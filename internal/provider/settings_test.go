package provider

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"qoderwake-panel/internal/apierror"
)

func setupSettings(t *testing.T) *Settings {
	t.Helper()
	root := t.TempDir()
	return NewSettings(filepath.Join(root, "settings.json"), filepath.Join(root, "recovery.json"))
}
func TestSettingsPreserveUnknownFieldsAndBlankKey(t *testing.T) {
	s := setupSettings(t)
	raw := []byte(`{"other":{"large":9007199254740995},"providers":{"local":{"type":"openai-compatible","authType":"bearer","apiKey":"synthetic-key-for-tests","baseUrl":"https://example.com/v1","thinking":true,"models":[{"model":"old","extra":"keep"}]}}}`)
	if e := os.WriteFile(s.Path, raw, 0600); e != nil {
		t.Fatal(e)
	}
	before, e := s.Read()
	if e != nil {
		t.Fatal(e)
	}
	after, e := s.Write("local", "https://example.com/v1", "", "new", "New", before.Revision)
	if e != nil {
		t.Fatal(e)
	}
	provider := after.Data["providers"].(map[string]any)["local"].(map[string]any)
	if provider["apiKey"] != "synthetic-key-for-tests" || provider["thinking"] != true {
		t.Fatal("provider fields lost")
	}
	if after.Data["other"].(map[string]any)["large"].(json.Number).String() != "9007199254740995" {
		t.Fatal("large integer changed")
	}
	model := provider["models"].([]any)[0].(map[string]any)
	if model["model"] != "new" || model["extra"] != "keep" {
		t.Fatal("model fields lost")
	}
	backup, e := os.ReadFile(s.Recovery)
	if e != nil || string(backup) != string(raw) {
		t.Fatal("recovery does not contain original bytes")
	}
	st, _ := os.Stat(s.Recovery)
	if st.Mode().Perm() != 0600 {
		t.Fatal("recovery permission mismatch")
	}
	rows, rev, e := s.Summaries()
	if e != nil || rev != after.Revision || len(rows) != 1 {
		t.Fatal(e)
	}
	encoded, _ := json.Marshal(rows)
	if strings.Contains(string(encoded), "synthetic-key-for-tests") {
		t.Fatal("secret in summary")
	}
}
func TestStaleAndExternalChangesCannotBeOverwritten(t *testing.T) {
	s := setupSettings(t)
	before, _ := s.Read()
	if e := os.WriteFile(s.Path, []byte(`{"providers":{},"external":true}`), 0600); e != nil {
		t.Fatal(e)
	}
	_, e := s.Write("new", "https://example.com/v1", "synthetic-key", "model", "", before.Revision)
	var ae *apierror.Error
	if !errors.As(e, &ae) || ae.Code != "version_conflict" {
		t.Fatal("stale revision accepted", e)
	}
	fresh, _ := s.Read()
	_, e = s.Update(fresh.Revision, func(data map[string]any) error {
		return os.WriteFile(s.Path, []byte(`{"providers":{},"external":2}`), 0600)
	})
	if !errors.As(e, &ae) || ae.Code != "version_conflict" {
		t.Fatal("external writer overwritten", e)
	}
}
func TestComplexProviderIsReadOnlyAndSymlinkRejected(t *testing.T) {
	s := setupSettings(t)
	raw := []byte(`{"providers":{"complex":{"type":"anthropic","models":[{"model":"one"}],"apiKey":"synthetic-key"}}}`)
	os.WriteFile(s.Path, raw, 0600)
	before, _ := s.Read()
	_, e := s.Write("complex", "https://example.com", "", "new", "", before.Revision)
	var ae *apierror.Error
	if !errors.As(e, &ae) || ae.Code != "provider_requires_official_editor" {
		t.Fatal(e)
	}
	os.Remove(s.Path)
	target := filepath.Join(t.TempDir(), "target")
	os.WriteFile(target, raw, 0600)
	os.Symlink(target, s.Path)
	if _, e = s.Read(); e == nil {
		t.Fatal("settings symlink accepted")
	}
}
func TestProbePlanIsBoundAndSingleUse(t *testing.T) {
	plans := NewPlans()
	p, e := plans.Create("user:a", "adhoc", "", "", "https://example.com/v1", "synthetic-a")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = plans.Consume(p.ID, "user:b", "adhoc", "", "", "https://example.com/v1", "synthetic-a"); e == nil {
		t.Fatal("foreign plan accepted")
	}
	if _, e = plans.Consume(p.ID, "user:a", "adhoc", "", "", "https://example.com/v1", "synthetic-b"); e == nil {
		t.Fatal("changed credential accepted")
	}
	if _, e = plans.Consume(p.ID, "user:a", "adhoc", "", "", "https://example.com/v1", "synthetic-a"); e == nil {
		t.Fatal("consumed plan reused")
	}
}
func TestProviderProbeDestinationRules(t *testing.T) {
	for _, values := range [][]string{{"10.0.0.1"}, {"169.254.169.254"}, {"127.0.0.1"}, {"::ffff:127.0.0.1"}, {"8.8.8.8", "192.168.1.1"}} {
		ips := []netip.Addr{}
		for _, s := range values {
			ips = append(ips, netip.MustParseAddr(s))
		}
		if AllowedAddresses("api.example.com", ips) == nil {
			t.Fatal("private or mixed DNS accepted")
		}
	}
	if e := AllowedAddresses("localhost", []netip.Addr{netip.MustParseAddr("127.0.0.1")}); e != nil {
		t.Fatal(e)
	}
	if AllowedAddresses("localhost", []netip.Addr{netip.MustParseAddr("8.8.8.8")}) == nil {
		t.Fatal("loopback host resolved to public accepted")
	}
}
func TestProbeDoesNotFollowRedirects(t *testing.T) {
	reached := false
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached = true; w.Write([]byte(`{"data":[]}`)) }))
	defer target.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", target.URL)
		w.WriteHeader(302)
	}))
	defer origin.Close()
	ok, message, e := Probe(context.Background(), origin.URL, "synthetic-key")
	if e != nil || ok || !strings.Contains(message, "302") || reached {
		t.Fatal("redirect probe mismatch", e)
	}
	success := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" || r.Header.Get("Authorization") != "Bearer synthetic-key" {
			t.Error("wrong request target")
		}
		w.Write([]byte(`{"data":[{"id":"example-model"}]}`))
	}))
	defer success.Close()
	ok, message, e = Probe(context.Background(), success.URL+"/v1", "synthetic-key")
	if e != nil || !ok || !strings.Contains(message, "example-model") {
		t.Fatal(e)
	}
}
