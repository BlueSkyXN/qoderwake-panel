package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"qoderwake-panel/internal/apierror"
	"qoderwake-panel/internal/daemon"
	"qoderwake-panel/internal/deletion"
)

type daemonFunc func(context.Context, string, string, any) (map[string]any, error)

func (f daemonFunc) JSON(ctx context.Context, method, path string, body any) (map[string]any, error) {
	return f(ctx, method, path, body)
}

type deletionFixture struct {
	name, failure    string
	deleted          bool
	writes, response int
}

func (f *deletionFixture) JSON(_ context.Context, method, path string, _ any) (map[string]any, error) {
	if f.failure != "" && strings.HasPrefix(path, f.failure) {
		return nil, apierror.New(502, "backend_unavailable")
	}
	var data any
	switch {
	case path == "/api/agents/w1" && method == "DELETE":
		f.writes++
		if f.response != 0 {
			return nil, &daemon.Error{Status: f.response, Code: "daemon_resource_not_found"}
		}
		f.deleted = true
		data = map[string]any{}
	case path == "/api/agents/w1":
		if f.deleted {
			return nil, &daemon.Error{Status: 404, Code: "daemon_resource_not_found"}
		}
		data = map[string]any{"agentId": "w1", "name": f.name, "description": "", "skills": []any{}, "systemIdentity": nil}
	case strings.HasPrefix(path, "/api/triggers?"):
		data = map[string]any{"items": []any{}, "pagination": map[string]any{"total": 0}}
	case path == "/api/channels", path == "/api/agents/w1/workflows", path == "/api/workflows":
		data = []any{}
	case strings.HasPrefix(path, "/api/channels/pairing/pending?"):
		data = map[string]any{"items": []any{}, "total": 0}
	case path == "/api/conversation-groups":
		data = map[string]any{"groups": []any{}}
	case strings.HasPrefix(path, "/api/agents/w1/console-sessions/query?"):
		data = map[string]any{"items": []any{}, "has_more": false}
	case path == "/api/v1/system/status":
		data = map[string]any{"activity": map[string]any{"runningConversationTasks": 0, "runningTriggerTasks": 0, "queuedTriggerTasks": 0}}
	case path == "/api/health":
		data = map[string]any{"version": "1.1.13"}
	case path == "/api/agents":
		data = []any{map[string]any{"agentId": "w1", "name": f.name}}
	case path == "/api/model-preferences/w1":
		data = map[string]any{"noProject": "auto", "byProject": map[string]any{}}
	default:
		return nil, fmt.Errorf("unexpected upstream request: %s %s", method, path)
	}
	return map[string]any{"data": data}, nil
}

func TestHTTPDeletionPreviewReplayAndRecovery(t *testing.T) {
	for _, response := range []int{0, 202, 502, 404} {
		t.Run(strconv.Itoa(response), func(t *testing.T) {
			s, _ := testServer(t)
			fake := &deletionFixture{name: "Original", response: response}
			s.Daemon = fake
			raw, csrf := login(t, s, "owner")
			preview := request(t, s, "POST", "/api/deletion/preview", raw, csrf, map[string]any{"kind": "waker", "target": "w1"})
			var impact struct {
				deletion.Impact
				ID string `json:"id"`
			}
			if preview.Code != 200 || json.Unmarshal(preview.Body.Bytes(), &impact) != nil || !impact.Deletable || impact.ID == "" {
				t.Fatal("preview failed", preview.Code, preview.Body.String())
			}
			body := map[string]any{"id": "w1", "preview": impact.ID, "impactDigest": impact.ImpactDigest, "confirm": "/api/waker/delete", "acknowledgedWarnings": []string{}}
			headers := map[string]string{"Idempotency-Key": "fixture-delete-0001"}
			unconfirmed := request(t, s, "POST", "/api/waker/delete", raw, csrf, body, headers)
			if unconfirmed.Code != 409 || fake.writes != 0 {
				t.Fatal("warning was not required")
			}
			body["acknowledgedWarnings"] = impact.WarningsRequired
			fake.name = "Changed"
			stale := request(t, s, "POST", "/api/waker/delete", raw, csrf, body, headers)
			if stale.Code != 409 || fake.writes != 0 {
				t.Fatal("stale target deleted")
			}
			fake.name = "Original"
			first := request(t, s, "POST", "/api/waker/delete", raw, csrf, body, headers)
			expect := 200
			if response == 202 || response == 502 {
				expect = 202
			} else if response == 404 {
				expect = 404
			}
			if first.Code != expect || fake.writes != 1 {
				t.Fatal("unexpected deletion response", first.Code, first.Body.String())
			}
			replay := request(t, s, "POST", "/api/waker/delete", raw, csrf, body, headers)
			if fake.writes != 1 || !strings.Contains(replay.Body.String(), `"replay":true`) {
				t.Fatal("delete was replayed", replay.Body.String())
			}
			status := request(t, s, "POST", "/api/deletion/status", raw, csrf, map[string]any{"idempotencyKey": "fixture-delete-0001"})
			if status.Code != 200 {
				t.Fatal("operation unavailable")
			}
			if response == 202 || response == 502 {
				blocked := request(t, s, "POST", "/api/deletion/preview", raw, csrf, map[string]any{"kind": "waker", "target": "w1"})
				if blocked.Code != 409 {
					t.Fatal("unresolved target not locked")
				}
				write := request(t, s, "POST", "/api/preference", raw, csrf, map[string]any{"wakerId": "w1", "model": "auto"})
				if write.Code != 409 {
					t.Fatal("dependent write not locked")
				}
				var ok, code int
				if err := s.Auth.DB.QueryRow("SELECT ok,status FROM audit WHERE action='/api/waker/delete' ORDER BY rowid DESC LIMIT 1").Scan(&ok, &code); err != nil || ok != 0 || code != 202 {
					t.Fatal("unknown result audited as success", ok, code, err)
				}
				reconcile := request(t, s, "POST", "/api/deletion/reconcile", raw, csrf, map[string]any{"kind": "waker", "target": "w1"})
				if reconcile.Code != 202 || !strings.Contains(reconcile.Body.String(), `"reconciled":false`) {
					t.Fatal("existing target resolved")
				}
				if response == 202 {
					fake.deleted = true
					reconcile = request(t, s, "POST", "/api/deletion/reconcile", raw, csrf, map[string]any{"kind": "waker", "target": "w1"})
					if reconcile.Code != 200 {
						t.Fatal("absent target not resolved")
					}
				} else {
					release := map[string]any{"kind": "waker", "target": "w1", "decision": "failed", "targetConfirmation": "wrong", "confirm": "/api/deletion/release"}
					if request(t, s, "POST", "/api/deletion/release", raw, csrf, release).Code != 400 {
						t.Fatal("wrong target confirmation accepted")
					}
					release["targetConfirmation"] = "w1"
					if request(t, s, "POST", "/api/deletion/release", raw, csrf, release).Code != 200 {
						t.Fatal("release failed")
					}
				}
				locked, err := s.Auth.HasUnresolvedDeletions()
				if err != nil || locked {
					t.Fatal("target stayed locked", err)
				}
			}
		})
	}
}

func TestHTTPDeletionRequiredSourceAndRole(t *testing.T) {
	s, _ := testServer(t)
	s.Daemon = &deletionFixture{name: "Waker", failure: "/api/channels"}
	raw, csrf := login(t, s, "owner")
	w := request(t, s, "POST", "/api/deletion/preview", raw, csrf, map[string]any{"kind": "waker", "target": "w1"})
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"deletable":false`) || !strings.Contains(w.Body.String(), `"id":null`) {
		t.Fatal("missing dependencies did not block", w.Body.String())
	}
	if _, err := s.Auth.CreateUser("editor", "Editor", "maintainer", []string{"w1"}, "example-password-for-tests", false); err != nil {
		t.Fatal(err)
	}
	raw, csrf = login(t, s, "editor")
	if request(t, s, "POST", "/api/deletion/preview", raw, csrf, map[string]any{"kind": "waker", "target": "w1"}).Code != 403 {
		t.Fatal("maintainer could delete")
	}
}

func TestDeletionTriggerPaginationCompatibility(t *testing.T) {
	s, _ := testServer(t)
	pages := 0
	s.Daemon = daemonFunc(func(_ context.Context, _ string, path string, _ any) (map[string]any, error) {
		u, _ := url.Parse(path)
		page, _ := strconv.Atoi(u.Query().Get("page"))
		pages++
		return map[string]any{"data": map[string]any{"items": []any{map[string]any{"id": strconv.Itoa(page)}}, "pagination": map[string]any{"total": 101}}}, nil
	})
	rows, err := s.deletionPages(context.Background(), "triggers", "")
	if err != nil || len(rows) != 101 || pages != 101 {
		t.Fatal("sparse pages not fully scanned", pages, err)
	}
	s.Daemon = daemonFunc(func(context.Context, string, string, any) (map[string]any, error) {
		return map[string]any{"data": []any{map[string]any{"id": "single"}}}, nil
	})
	rows, err = s.deletionPages(context.Background(), "triggers", "")
	if err != nil || len(rows) != 1 {
		t.Fatal("legacy unpaged response rejected", err)
	}
}
