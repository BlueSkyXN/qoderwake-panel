package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"qoderwake-panel/internal/deletion"
)

func TestHTTPProviderRevisionProbeAndDeletion(t *testing.T) {
	s, _ := testServer(t)
	s.Daemon = &deletionFixture{name: "Waker"}
	raw, csrf := login(t, s, "owner")
	before, e := s.Providers.Read()
	if e != nil {
		t.Fatal(e)
	}
	calls := 0
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "GET" || r.URL.Path != "/v1/models" || r.Header.Get("Authorization") != "Bearer synthetic-provider-key" {
			t.Error("probe contract changed")
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":[{"id":"model"}]}`))
	}))
	defer endpoint.Close()
	body := map[string]any{"name": "synthetic", "baseUrl": endpoint.URL + "/v1", "apiKey": "synthetic-provider-key", "model": "model", "display": "示例", "baseRevision": before.Revision}
	saved := request(t, s, "POST", "/api/provider", raw, csrf, body)
	if saved.Code != 200 {
		t.Fatal("save", saved.Code, saved.Body.String())
	}
	if request(t, s, "POST", "/api/provider", raw, csrf, body).Code != 409 {
		t.Fatal("stale revision accepted")
	}
	after, _ := s.Providers.Read()
	state := request(t, s, "GET", "/api/state", raw, csrf, nil)
	if state.Code != 200 || strings.Contains(state.Body.String(), "synthetic-provider-key") || !strings.Contains(state.Body.String(), `"keyConfigured":true`) {
		t.Fatal("state exposed secret or omitted provider")
	}
	payload := map[string]any{"mode": "saved", "name": "synthetic", "baseRevision": after.Revision}
	plan := request(t, s, "POST", "/api/provider/probe-plan", raw, csrf, payload)
	var p struct {
		ID     string
		Target string
	}
	if json.Unmarshal(plan.Body.Bytes(), &p) != nil || p.ID == "" || calls != 0 {
		t.Fatal("plan sent a request or failed", plan.Body.String())
	}
	payload["plan"] = p.ID
	payload["confirm"] = "/api/test/saved"
	if request(t, s, "POST", "/api/test/saved", raw, csrf, payload).Code != 200 || calls != 1 {
		t.Fatal("probe failed")
	}
	if request(t, s, "POST", "/api/test/saved", raw, csrf, payload).Code != 400 || calls != 1 {
		t.Fatal("probe was replayed")
	}
	impactResponse := request(t, s, "POST", "/api/deletion/preview", raw, csrf, map[string]any{"kind": "provider", "target": "synthetic", "baseRevision": after.Revision})
	var impact struct {
		deletion.Impact
		ID string `json:"id"`
	}
	if impactResponse.Code != 200 || json.Unmarshal(impactResponse.Body.Bytes(), &impact) != nil || !impact.Deletable {
		t.Fatal("provider preview", impactResponse.Body.String())
	}
	deleted := request(t, s, "POST", "/api/provider/delete", raw, csrf, map[string]any{"name": "synthetic", "baseRevision": after.Revision, "preview": impact.ID, "impactDigest": impact.ImpactDigest, "acknowledgedWarnings": impact.WarningsRequired, "confirm": "/api/provider/delete"}, map[string]string{"Idempotency-Key": "provider-delete-0001"})
	if deleted.Code != 200 {
		t.Fatal("provider delete", deleted.Code, deleted.Body.String())
	}
	snapshot, _ := s.Providers.Read()
	if len(snapshot.Data["providers"].(map[string]any)) != 0 {
		t.Fatal("provider was not removed")
	}
}

func TestHomeOverrideResolvesDaemonPortAndCLI(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "custom-home")
	if e := os.MkdirAll(filepath.Join(home, "run"), 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(home, "run/daemon.port"), []byte("19837\n"), 0600); e != nil {
		t.Fatal(e)
	}
	s, e := New(Config{Root: root, Home: home})
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	if s.Config.DaemonURL != "http://127.0.0.1:19837" || s.Config.CLI != filepath.Join(home, "qoderwake") {
		t.Fatal("home override ignored")
	}
}

func TestCreateWakerReturnsPartialModelFailureWithoutRepeatingCreate(t *testing.T) {
	s, _ := testServer(t)
	created := 0
	s.Daemon = daemonFunc(func(_ context.Context, method, path string, body any) (map[string]any, error) {
		if method != "POST" || path != "/api/agents" || body.(map[string]any)["name"] != "测试 Waker" {
			t.Fatal("invalid create contract")
		}
		created++
		return map[string]any{"data": map[string]any{"agentId": "new-waker"}}, nil
	})
	raw, csrf := login(t, s, "owner")
	result := request(t, s, "POST", "/api/waker", raw, csrf, map[string]any{"name": "测试 Waker", "model": "auto"})
	if result.Code != 200 || created != 1 || !strings.Contains(result.Body.String(), `"preferenceApplied":false`) {
		t.Fatal("partial failure lost", result.Body.String())
	}
}
