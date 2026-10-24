package server

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"qoderwake-panel/internal/apierror"
	"qoderwake-panel/internal/daemon"
)

func TestNativeBYOKBoundaryNeverWritesLegacySettings(t *testing.T) {
	s, _ := testServer(t)
	os.MkdirAll(filepath.Dir(s.Providers.Path), 0700)
	os.WriteFile(s.Providers.Path, []byte(`{"providers":{},"unrelated":true}`), 0600)
	before, _ := os.ReadFile(s.Providers.Path)
	s.Daemon = daemonFunc(func(_ context.Context, method, path string, body any) (map[string]any, error) {
		if method != "GET" || path != "/api/models/byok" || body != nil {
			t.Fatal("unexpected upstream write")
		}
		return nil, &daemon.Error{Status: 403, Code: "MODEL_SETTINGS_LOCAL_ONLY"}
	})
	raw, csrf := login(t, s, "owner")
	w := request(t, s, "GET", "/api/models/native-byok", raw, csrf, nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"status":"local_only"`) || !strings.Contains(w.Body.String(), `"writable":false`) {
		t.Fatal("local-only state lost", w.Body.String())
	}
	after, _ := os.ReadFile(s.Providers.Path)
	if string(before) != string(after) {
		t.Fatal("native fallback wrote legacy settings")
	}
	s.Daemon = daemonFunc(func(_ context.Context, method, path string, body any) (map[string]any, error) {
		return map[string]any{"data": map[string]any{"configs": []any{map[string]any{"apiKey": "synthetic-native-secret", "models": []any{map[string]any{"maxOutputTokens": 64000}}}}}}, nil
	})
	w = request(t, s, "GET", "/api/models/native-byok", raw, csrf, nil)
	if strings.Contains(w.Body.String(), "synthetic-native-secret") || !strings.Contains(w.Body.String(), "64000") {
		t.Fatal("native metadata redaction")
	}
}

func TestBindingProjectReadbackConflictAndSessionIsolation(t *testing.T) {
	s, _ := testServer(t)
	prefs := map[string]any{"noProject": map[string]any{"modelId": "custom/one", "reasoningEffort": "high"}, "byProject": map[string]any{"project-a": map[string]any{"modelId": "custom/two", "contextWindow": 500000}}}
	writes := 0
	s.Daemon = daemonFunc(func(_ context.Context, method, path string, body any) (map[string]any, error) {
		switch path {
		case "/api/model-preferences/w1":
			return map[string]any{"data": prefs}, nil
		case "/api/model-preferences/w1/entry":
			if method != "PUT" {
				t.Fatal("wrong preference contract")
			}
			p := body.(map[string]any)
			if p["projectId"] != "project-a" || p["contextWindow"] != 500000 {
				t.Fatal("project or parameters dropped", p)
			}
			writes++
			prefs["byProject"].(map[string]any)["project-a"] = map[string]any{"modelId": p["model"], "contextWindow": 500000}
			return map[string]any{"data": map[string]any{}}, nil
		case "/api/sessions":
			return map[string]any{"data": []any{
				map[string]any{"agentId": "w1", "sessionId": "s1", "currentModel": "official/model", "runtimeRestartRequired": true, "currentModelRuntimeConfig": map[string]any{"model": "official/model", "apiKey": "synthetic-session-secret"}},
				map[string]any{"agentId": "w2", "sessionId": "foreign-session", "currentModel": "private/model"},
			}}, nil
		}
		return nil, apierror.New(404, "not_found")
	})
	owner, csrf := login(t, s, "owner")
	w := request(t, s, "GET", "/api/waker/models?waker=w1", owner, csrf, nil)
	if w.Code != 200 || strings.Contains(w.Body.String(), "foreign-session") || strings.Contains(w.Body.String(), "synthetic-session-secret") || !strings.Contains(w.Body.String(), `"currentModel":"official/model"`) {
		t.Fatal("session filtering or separation failed", w.Code)
	}
	var data struct{ Preferences struct{ Revision string } }
	json.Unmarshal(w.Body.Bytes(), &data)
	payload := map[string]any{"wakerId": "w1", "projectId": "project-a", "model": "custom/three", "baseRevision": data.Preferences.Revision, "confirm": "/api/waker/models/update"}
	w = request(t, s, "POST", "/api/waker/models/update", owner, csrf, payload)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"status":"verified"`) || writes != 1 {
		t.Fatal("preference not verified", w.Body.String())
	}
	if prefs["noProject"].(map[string]any)["modelId"] != "custom/one" {
		t.Fatal("other scope overwritten")
	}
	w = request(t, s, "POST", "/api/waker/models/update", owner, csrf, payload)
	if w.Code != 409 || writes != 1 {
		t.Fatal("stale preference overwritten")
	}
	for _, role := range []string{"maintainer", "observer"} {
		s.Auth.CreateUser(role, role, role, []string{"w1"}, "example-password-for-tests", false)
		user, token := login(t, s, role)
		if request(t, s, "GET", "/api/waker/models?waker=w1", user, token, nil).Code != 200 {
			t.Fatal("assigned Waker read failed")
		}
		if request(t, s, "GET", "/api/waker/models?waker=w2", user, token, nil).Code != 403 {
			t.Fatal("cross Waker read allowed")
		}
		if request(t, s, "POST", "/api/waker/models/update", user, token, payload).Code != 403 {
			t.Fatal("nonadmin binding write allowed")
		}
		if request(t, s, "GET", "/api/provider/config?name=multi", user, token, nil).Code != 403 {
			t.Fatal("nonadmin credential read allowed")
		}
	}
}

func TestPreferenceUnknownAndReadbackFailuresAreNotSuccessfulSwitches(t *testing.T) {
	for _, mode := range []string{"unknown", "saved_unverified", "readback_mismatch"} {
		t.Run(mode, func(t *testing.T) {
			s, _ := testServer(t)
			written := false
			writes := 0
			s.Daemon = daemonFunc(func(_ context.Context, method, path string, b any) (map[string]any, error) {
				if method == "PUT" {
					writes++
					written = true
					if mode == "unknown" {
						return nil, apierror.New(502, "daemon_unavailable")
					}
					return map[string]any{}, nil
				}
				if written && mode == "saved_unverified" {
					return nil, apierror.New(502, "daemon_unavailable")
				}
				return map[string]any{"data": map[string]any{"noProject": "custom/one", "byProject": map[string]any{}}}, nil
			})
			before, _ := s.modelPreferences(context.Background(), "w1")
			raw, csrf := login(t, s, "owner")
			w := request(t, s, "POST", "/api/waker/models/update", raw, csrf, map[string]any{"wakerId": "w1", "model": "custom/two", "baseRevision": before["revision"], "confirm": "/api/waker/models/update"})
			if w.Code != 202 || !strings.Contains(w.Body.String(), `"status":"`+mode+`"`) || writes != 1 {
				t.Fatal("false success or replay", w.Body.String())
			}
		})
	}
}

func TestProviderJSONEndpointRedactionAndTwoEditors(t *testing.T) {
	s, _ := testServer(t)
	os.MkdirAll(filepath.Dir(s.Providers.Path), 0700)
	os.WriteFile(s.Providers.Path, []byte(`{"providers":{"multi":{"baseUrl":"http://models.example.test/v1","apiKey":"synthetic-http-secret","protocol":"openai-responses","models":[{"model":"one"},{"model":"two"}]}}}`), 0600)
	owner, csrf := login(t, s, "owner")
	w := request(t, s, "GET", "/api/provider/config?name=multi", owner, csrf, nil)
	var draft struct{ ConfigJSON, BaseRevision string }
	json.Unmarshal(w.Body.Bytes(), &draft)
	if w.Code != 200 || draft.ConfigJSON == "" || strings.Contains(w.Body.String(), "synthetic-http-secret") {
		t.Fatal("editor missing or leaked")
	}
	payload := map[string]any{"name": "multi", "configJSON": draft.ConfigJSON, "keyAction": "replace", "apiKey": "synthetic-new-secret", "baseRevision": draft.BaseRevision, "confirm": "/api/provider/config"}
	w = request(t, s, "POST", "/api/provider/config", owner, csrf, payload)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"status":"verified"`) || strings.Contains(w.Body.String(), "synthetic-new-secret") {
		t.Fatal("config write not verified", w.Body.String())
	}
	if request(t, s, "POST", "/api/provider/config", owner, csrf, payload).Code != 409 {
		t.Fatal("stale config overwrite")
	}
	// Removing a model requires complete dependency checks, even when JSON is edited directly.
	latest, _ := s.Providers.Editor("multi")
	payload["baseRevision"] = latest["baseRevision"]
	payload["configJSON"] = strings.Replace(latest["configJSON"].(string), `"model": "two"`, `"model": "renamed"`, 1)
	w = request(t, s, "POST", "/api/provider/config", owner, csrf, payload)
	if w.Code != 409 || !strings.Contains(w.Body.String(), "provider_models_in_use") {
		t.Fatal("model removal bypassed dependency scan", w.Code)
	}
	unchanged, _ := s.Providers.Editor("multi")
	if unchanged["baseRevision"] != latest["baseRevision"] {
		t.Fatal("blocked removal changed settings")
	}
	audit := request(t, s, "GET", "/api/audit", owner, csrf, nil)
	if strings.Contains(audit.Body.String(), "synthetic-new-secret") || strings.Contains(audit.Body.String(), "configJSON") {
		t.Fatal("audit leaked edit body")
	}
}
