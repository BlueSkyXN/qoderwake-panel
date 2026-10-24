package server

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"qoderwake-panel/internal/auth"
)

func TestStateCacheInvalidationDuringRefresh(t *testing.T) {
	s, _ := testServer(t)
	started, proceed := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	s.Daemon = daemonFunc(func(_ context.Context, _ string, path string, _ any) (map[string]any, error) {
		if path == "/api/models" {
			n := calls.Add(1)
			if n == 1 {
				close(started)
				<-proceed
			}
			model := "new"
			if n == 1 {
				model = "stale"
			}
			return map[string]any{"data": map[string]any{"models": []any{map[string]any{"id": model}}}}, nil
		}
		if path == "/api/agents" {
			return map[string]any{"data": []any{}}, nil
		}
		return map[string]any{"data": map[string]any{}}, nil
	})
	result := make(chan map[string]any, 1)
	fail := make(chan error, 1)
	go func() {
		value, e := s.cachedState(context.Background())
		if e != nil {
			fail <- e
		} else {
			result <- value
		}
	}()
	<-started
	s.invalidateState()
	close(proceed)
	select {
	case e := <-fail:
		t.Fatal(e)
	case value := <-result:
		raw, _ := json.Marshal(value)
		if strings.Contains(string(raw), "stale") || !strings.Contains(string(raw), "new") {
			t.Fatal("stale refresh published")
		}
	}
	if _, e := s.cachedState(context.Background()); e != nil || calls.Load() != 2 {
		t.Fatal("state cache failed", e, calls.Load())
	}
}

func TestCachedStateIsProjectedPerAccount(t *testing.T) {
	s, _ := testServer(t)
	s.Daemon = daemonFunc(func(_ context.Context, _ string, path string, _ any) (map[string]any, error) {
		switch path {
		case "/api/models":
			return map[string]any{"data": map[string]any{"models": []any{map[string]any{"id": "private-model"}}}}, nil
		case "/api/agents":
			return map[string]any{"data": []any{map[string]any{"agentId": "w1", "name": "assigned"}, map[string]any{"agentId": "w2", "name": "unassigned"}}}, nil
		case "/api/frontend-auth/me":
			return map[string]any{"data": map[string]any{"authenticated": true, "profile": map[string]any{"name": "private-official-owner"}}}, nil
		}
		return map[string]any{"data": map[string]any{"noProject": "auto", "byProject": map[string]any{}}}, nil
	})
	admin, e := s.fullState(context.Background(), auth.Identity{Role: "admin"})
	if e != nil {
		t.Fatal(e)
	}
	if admin["whoami"] == nil {
		t.Fatal("admin profile missing")
	}
	observer, e := s.fullState(context.Background(), auth.Identity{Role: "observer", UserID: "example", Wakers: []string{"w1"}})
	if e != nil {
		t.Fatal(e)
	}
	raw, _ := json.Marshal(observer)
	for _, hidden := range []string{"unassigned", "private-model", "private-official-owner", "telemetry", "guard"} {
		if strings.Contains(string(raw), hidden) {
			t.Fatal("cached admin state leaked", hidden)
		}
	}
	again, e := s.fullState(context.Background(), auth.Identity{Role: "admin"})
	if e != nil || again["whoami"] == nil {
		t.Fatal("projection changed shared state")
	}
}
func TestWhitelistMetadataIsNotExposedAsAddress(t *testing.T) {
	s, _ := testServer(t)
	path := filepath.Join(s.Config.Root, "whitelist.json")
	if e := os.WriteFile(path, []byte(`{"_readme":"developer-only instructions","203.0.113.1":"Fixture"}`), 0600); e != nil {
		t.Fatal(e)
	}
	s.Config.Whitelist = path
	rows := s.whitelist()
	if len(rows) != 1 || rows[0]["ip"] != "203.0.113.1" {
		t.Fatal("metadata exposed in address table")
	}
}
