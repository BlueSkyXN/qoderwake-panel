package cli

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"qoderwake-panel/internal/apierror"
	"qoderwake-panel/internal/auth"
)

func fakeJobs(t *testing.T, operation, behavior string) (*Jobs, Spec, string) {
	t.Helper()
	catalog, e := NewCatalog()
	if e != nil {
		t.Fatal(e)
	}
	spec, e := catalog.Get(operation)
	if e != nil {
		t.Fatal(e)
	}
	dir := t.TempDir()
	help := filepath.Join(dir, "help")
	binary := filepath.Join(dir, "qoderwake")
	counter := filepath.Join(dir, "runs")
	if e = os.WriteFile(help, []byte(spec.Help), 0600); e != nil {
		t.Fatal(e)
	}
	script := "#!/bin/sh\nfor value in \"$@\"; do if [ \"$value\" = '--help' ]; then cat '" + help + "'; exit 0; fi; done\nprintf 'run\\n' >> '" + counter + "'\n" + behavior + "\n"
	if e = os.WriteFile(binary, []byte(script), 0700); e != nil {
		t.Fatal(e)
	}
	jobs, e := OpenJobs(filepath.Join(dir, "jobs"), dir, binary, catalog)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { jobs.Close() })
	return jobs, spec, counter
}
func requestFor(t *testing.T, spec Spec, params map[string]any) Request {
	t.Helper()
	req, e := DecodeRequest(map[string]any{"operation": spec.ID, "revision": spec.Revision, "confirm": spec.ID, "timeout": json.Number("10"), "parameters": params})
	if e != nil {
		t.Fatal(e)
	}
	return req
}

var owner = auth.Identity{Role: "admin", Principal: "user:example", UserID: "example"}

func awaitJob(t *testing.T, j *Jobs, id string) Output {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		out, e := j.Get(owner, id, 0)
		if e != nil {
			t.Fatal(e)
		}
		if out.Status != "queued" && out.Status != "running" {
			return out
		}
		time.Sleep(15 * time.Millisecond)
	}
	t.Fatal("test command did not complete")
	return Output{}
}
func TestJobsIdempotencyAndOutput(t *testing.T) {
	j, spec, counter := fakeJobs(t, "status", "printf 'hello 中文\\n'")
	req := requestFor(t, spec, map[string]any{})
	first, e := j.Submit(context.Background(), owner, req, "example-request-0001")
	if e != nil {
		t.Fatal(e)
	}
	second, e := j.Submit(context.Background(), owner, req, "example-request-0001")
	if e != nil || second.ID != first.ID {
		t.Fatal("request duplicated", e)
	}
	result := awaitJob(t, j, first.ID)
	if result.Status != "succeeded" || !strings.Contains(result.Output, "hello 中文") {
		t.Fatal("command result mismatch")
	}
	runs, e := os.ReadFile(counter)
	if e != nil || string(runs) != "run\n" {
		t.Fatal("duplicate dispatch", e)
	}
	req.Raw["timeout"] = json.Number("20")
	_, e = j.Submit(context.Background(), owner, req, "example-request-0001")
	var ae *apierror.Error
	if !errors.As(e, &ae) || ae.Code != "idempotency_key_conflict" {
		t.Fatal(e)
	}
}
func TestWriteTimeoutRemainsUnknown(t *testing.T) {
	j, spec, _ := fakeJobs(t, "config.set", "sleep 30")
	req := requestFor(t, spec, map[string]any{"arguments": map[string]any{"key": "telemetry", "value": "false"}})
	req.Timeout = 100 * time.Millisecond
	result, e := j.Submit(context.Background(), owner, req, "example-write-0001")
	if e != nil {
		t.Fatal(e)
	}
	out := awaitJob(t, j, result.ID)
	if out.Status != "unknown" || out.Error != "cli_timeout" {
		t.Fatal("write timeout lost unknown state", out.Status, out.Error)
	}
}
func TestInteractivePromptInputAndRedaction(t *testing.T) {
	j, spec, _ := fakeJobs(t, "status", "printf 'Prompt: '; IFS= read -r value; printf 'reply=%s\\n' \"$value\"")
	req := requestFor(t, spec, map[string]any{})
	req.Interactive = true
	result, e := j.Submit(context.Background(), owner, req, "example-interactive-0001")
	if e != nil {
		t.Fatal(e)
	}
	deadline := time.Now().Add(4 * time.Second)
	prompt := false
	for time.Now().Before(deadline) {
		out, e := j.Get(owner, result.ID, 0)
		if e != nil {
			t.Fatal(e)
		}
		if out.CanInput && strings.Contains(out.Output, "Prompt:") {
			prompt = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !prompt {
		t.Fatal("PTY prompt missing")
	}
	secret := "synthetic-interactive-value"
	if e = j.Control(owner, result.ID, "input", secret+"\n"); e != nil {
		t.Fatal(e)
	}
	out := awaitJob(t, j, result.ID)
	if strings.Contains(out.Output, secret) || !strings.Contains(out.Output, "[已隐藏]") {
		t.Fatal("interactive value was not redacted")
	}
}
func TestReloadPreservesTaskIdentityWithoutReplaying(t *testing.T) {
	j, spec, counter := fakeJobs(t, "status", "printf 'done\\n'")
	req := requestFor(t, spec, map[string]any{})
	result, e := j.Submit(context.Background(), owner, req, "example-restart-0001")
	if e != nil {
		t.Fatal(e)
	}
	awaitJob(t, j, result.ID)
	// A second store is opened only after the original store has finished its work.
	root := filepath.Join(j.Home, "jobs")
	if e = j.Close(); e != nil {
		t.Fatal(e)
	}
	next, e := OpenJobs(root, j.Home, j.Binary, j.Catalog)
	if e != nil {
		t.Fatal(e)
	}
	defer next.Close()
	out, e := next.Get(owner, result.ID, 0)
	if e != nil || out.OutputAvailable || out.Status != "succeeded" {
		t.Fatal("history recovery mismatch", e)
	}
	replayed, e := next.Submit(context.Background(), owner, req, "example-restart-0001")
	if e != nil || replayed.ID != result.ID {
		t.Fatal("idempotency recovery failed", e)
	}
	runs, _ := os.ReadFile(counter)
	if string(runs) != "run\n" {
		t.Fatal("history replayed command")
	}
}
func TestEnvironmentAndRedactionBoundaries(t *testing.T) {
	j, _, _ := fakeJobs(t, "status", "true")
	t.Setenv("QODERWAKE_RUN_TOKEN", "synthetic-run-token")
	t.Setenv("BUN_BE_BUN", "1")
	for _, v := range j.Environment() {
		if strings.HasPrefix(v, "QODERWAKE_RUN_TOKEN=") || strings.HasPrefix(v, "BUN_BE_BUN=") {
			t.Fatal("ambient agent identity inherited")
		}
	}
	value := Redact("api_key=example-value\nBearer synthetic-token\nprivate-value", []string{"private-value"})
	for _, s := range []string{"example-value", "synthetic-token", "private-value"} {
		if strings.Contains(value, s) {
			t.Fatal("secret not redacted")
		}
	}
	visible, pending := partialOutput("Prompt: private-", []string{"private-value"})
	if visible != "Prompt: " || pending != "private-" {
		t.Fatal("split secret prefix leaked")
	}
}
