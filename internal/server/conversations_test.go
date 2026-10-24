package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"qoderwake-panel/internal/daemon"
)

func TestConversationTransformsMatchPython(t *testing.T) {
	py, e := exec.LookPath("python3")
	if e != nil {
		t.Skip("Python reference unavailable")
	}
	event := func(role, text, id string) map[string]any {
		return map[string]any{"payload": map[string]any{"type": role, "message": map[string]any{"id": id, "content": []any{map[string]any{"type": "text", "text": text}}}}}
	}
	events := []any{event("user", "one", "u1"), event("assistant", "part", "a1"), event("assistant", "final", "a1"), map[string]any{"payload": map[string]any{"type": "result", "subtype": "success"}}, event("user", "two", "u2"), event("assistant", "next", "a2")}
	doc := map[string]any{"path": "memory/notes.md", "content": strings.Repeat("记", 70000)}
	memory := map[string]any{"memoryId": "m1", "content": strings.Repeat("🙂", 60000), "privateBinding": "must-not-return"}
	input, _ := json.Marshal(map[string]any{"events": events, "doc": doc, "memory": memory})
	path, _ := filepath.Abs("../../tests")
	command := exec.Command(py, "-c", `import json,sys
sys.path.insert(0,sys.argv[1])
from test_panel import app
x=json.load(sys.stdin)
print(json.dumps([app.fold_events(x['events']),app.memory_doc(x['doc']),app.avatar_memory(x['memory'])],ensure_ascii=False))`, path)
	command.Stdin = bytes.NewReader(input)
	output, e := command.Output()
	if e != nil {
		t.Fatal("Python reference failed", e)
	}
	var expected any
	if e = json.Unmarshal(output, &expected); e != nil {
		t.Fatal(e)
	}
	document, e := memoryDoc(doc)
	if e != nil {
		t.Fatal(e)
	}
	record, e := avatarMemory(memory)
	if e != nil {
		t.Fatal(e)
	}
	actual := jsonValue([]any{foldEvents(events), document, record})
	if !reflect.DeepEqual(actual, expected) {
		t.Fatal("conversation/memory behavior differs from Python")
	}
	for _, path := range []string{"../private", "/absolute", ".hidden", "notes/../../private"} {
		if _, e = memoryDoc(map[string]any{"path": path, "content": "x"}); e == nil {
			t.Fatal("invalid memory path accepted")
		}
	}
}

func TestMemoryPartialSourcesAndIdentity(t *testing.T) {
	for _, failure := range []string{"graph", "files", "identity", "malformed", "none"} {
		t.Run(failure, func(t *testing.T) {
			s := &Server{Daemon: daemonFunc(func(_ context.Context, _ string, path string, _ any) (map[string]any, error) {
				if path == "/api/agents/w1" {
					id := "w1"
					if failure == "identity" {
						id = "other"
					}
					return map[string]any{"data": map[string]any{"agentId": id, "employeeType": "personal_avatar"}}, nil
				}
				if strings.HasSuffix(path, "/graph") {
					if failure == "graph" {
						return nil, &daemon.Conflict{Code: "PERSONAL_AVATAR_DWS_LOGIN_REQUIRED"}
					}
					if failure == "malformed" {
						return map[string]any{"data": map[string]any{"memories": []any{map[string]any{"memoryId": "m", "content": []any{}}}}}, nil
					}
					return map[string]any{"data": map[string]any{"memories": []any{map[string]any{"memoryId": "m", "content": "已有记忆"}}}}, nil
				}
				if failure == "files" {
					return nil, &daemon.Error{Status: 502, Code: "daemon_response_too_large"}
				}
				return map[string]any{"data": map[string]any{"scopeId": "w1", "index": map[string]any{"path": "MEMORY.md", "content": "旧文件"}}}, nil
			})}
			value, e := s.memoryData(context.Background(), "w1")
			if failure == "identity" {
				if e == nil {
					t.Fatal("identity mismatch accepted")
				}
				return
			}
			if e != nil {
				t.Fatal(e)
			}
			data := value.(map[string]any)
			if failure == "graph" || failure == "malformed" {
				if data["status"] != "unavailable" || data["filesStatus"] != "ready" || len(data["docs"].([]map[string]any)) != 1 {
					t.Fatal("lost readable memory files")
				}
				if failure == "graph" && data["error"] != "PERSONAL_AVATAR_DWS_LOGIN_REQUIRED" {
					t.Fatal("lost actionable graph error")
				}
			}
			if failure == "files" && (data["status"] != "ready" || data["filesStatus"] != "unavailable" || len(data["memories"].([]map[string]any)) != 1) {
				t.Fatal("lost readable graph")
			}
		})
	}
}

type artifactDaemon struct {
	daemonFunc
	downloads int
}

func (d *artifactDaemon) Raw(context.Context, string, string, any, int64) ([]byte, int, http.Header, error) {
	d.downloads++
	return []byte("<script>synthetic</script>"), 200, http.Header{"Content-Type": []string{"text/html; charset=utf-8"}}, nil
}
func TestSessionScopeArtifactMembershipAndMime(t *testing.T) {
	s, _ := testServer(t)
	d := &artifactDaemon{daemonFunc: func(_ context.Context, _ string, path string, _ any) (map[string]any, error) {
		if strings.Contains(path, "/console-sessions/query?") {
			return map[string]any{"data": map[string]any{"items": []any{map[string]any{"session_id": "s1", "local_agent_id": "w1", "secret": "hidden"}, map[string]any{"session_id": "s2", "local_agent_id": "w2"}}, "has_more": false}}, nil
		}
		return map[string]any{"data": map[string]any{"artifacts": []any{map[string]any{"id": "a1", "sourcePath": "hidden"}}}}, nil
	}}
	s.Daemon = d
	raw, csrf := login(t, s, "owner")
	list := request(t, s, "GET", "/api/sessions/query?waker=w1", raw, csrf, nil)
	if list.Code != 200 || strings.Contains(list.Body.String(), "s2") || strings.Contains(list.Body.String(), "hidden") {
		t.Fatal("session scope/filter failed")
	}
	file := request(t, s, "GET", "/api/session/artifact-file?session=s1&artifact=other", raw, csrf, nil)
	if file.Code != 404 || d.downloads != 0 {
		t.Fatal("unlisted artifact fetched")
	}
	file = request(t, s, "GET", "/api/session/artifact-file?session=s1&artifact=a1", raw, csrf, nil)
	if file.Code != 200 || d.downloads != 1 || file.Header().Get("Content-Type") != "application/octet-stream" || !strings.HasPrefix(file.Header().Get("Content-Disposition"), "attachment;") {
		t.Fatal("artifact active content not downloaded as attachment")
	}
	if _, e := s.Auth.CreateUser("observer", "Observer", "observer", []string{"w1"}, "example-password-for-tests", false); e != nil {
		t.Fatal(e)
	}
	raw, csrf = login(t, s, "observer")
	if request(t, s, "GET", "/api/sessions/query?waker=w1", raw, csrf, nil).Code != 200 || request(t, s, "GET", "/api/sessions/query?waker=w2", raw, csrf, nil).Code != 403 || request(t, s, "GET", "/api/session/artifacts?session=s1", raw, csrf, nil).Code != 403 {
		t.Fatal("scoped observer boundary failed")
	}
}

func TestPairingFreshnessAndPermissions(t *testing.T) {
	s, _ := testServer(t)
	revision := json.Number("9007199254740993")
	expires := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	pending := map[string]any{"pendingId": "p1", "pendingRevision": revision, "remoteExpiresAt": expires, "channelId": "c1", "senderId": "sender", "conversationId": "group", "bindingKey": "binding", "conversationType": "group"}
	writes := 0
	var sent map[string]any
	s.Daemon = daemonFunc(func(_ context.Context, method, path string, b any) (map[string]any, error) {
		if path == "/api/agents/w1" {
			return map[string]any{"data": map[string]any{"agentId": "w1"}}, nil
		}
		if method == "POST" {
			writes++
			sent = b.(map[string]any)
			return map[string]any{"success": true}, nil
		}
		return map[string]any{"data": map[string]any{"items": []any{pending}, "total": 1}}, nil
	})
	raw, csrf := login(t, s, "owner")
	body := map[string]any{}
	for k, v := range pending {
		body[k] = v
	}
	body["wakerId"] = "w1"
	body["confirm"] = "/api/pairing/approve"
	body["pendingRevision"] = json.Number("9007199254740992")
	if request(t, s, "POST", "/api/pairing/approve", raw, csrf, body).Code != 409 || writes != 0 {
		t.Fatal("stale pairing approved")
	}
	body["pendingRevision"] = revision
	pending["remoteExpiresAt"] = time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)
	if request(t, s, "POST", "/api/pairing/approve", raw, csrf, body).Code != 409 || writes != 0 {
		t.Fatal("expired pairing approved")
	}
	pending["remoteExpiresAt"] = expires
	if request(t, s, "POST", "/api/pairing/approve", raw, csrf, body).Code != 200 || writes != 1 {
		t.Fatal("fresh pairing failed")
	}
	if sent["allowCustomModel"] != false || sent["allowQoderwakeCommands"] != false || sent["workspace"] != nil {
		t.Fatal("pairing expanded capabilities")
	}
}

func TestGatewayCallerScopeQuotaAndReply(t *testing.T) {
	s, _ := testServer(t)
	calls := 0
	s.Daemon = daemonFunc(func(_ context.Context, method, path string, _ any) (map[string]any, error) {
		if method == "POST" {
			calls++
			if strings.HasSuffix(path, "/sessions") {
				return map[string]any{"data": map[string]any{"sessionId": "s1"}}, nil
			}
			return map[string]any{"success": true}, nil
		}
		return map[string]any{"data": []any{map[string]any{"payload": map[string]any{"type": "assistant", "message": map[string]any{"content": "合成回复"}}}, map[string]any{"payload": map[string]any{"type": "result", "subtype": "success"}}}}, nil
	})
	caller, e := s.Auth.CreateCaller("fixture", []string{"w1"}, 1, 1)
	if e != nil {
		t.Fatal(e)
	}
	headers := map[string]string{"Authorization": "Bearer " + caller["token"]}
	if request(t, s, "GET", "/api/state", "", "", nil, headers).Code != 403 {
		t.Fatal("caller accessed admin state")
	}
	if request(t, s, "POST", "/api/gw", "", "", map[string]any{"wakerId": "w2", "message": "hello"}, headers).Code != 403 || calls != 0 {
		t.Fatal("caller scope bypass")
	}
	reply := request(t, s, "POST", "/api/gw", "", "", map[string]any{"wakerId": "w1", "message": "hello"}, headers)
	if reply.Code != 200 || !strings.Contains(reply.Body.String(), "合成回复") || calls != 2 {
		t.Fatal("gateway request failed", reply.Code, reply.Body.String())
	}
	if request(t, s, "POST", "/api/gw", "", "", map[string]any{"wakerId": "w1", "message": "hello"}, headers).Code != 429 || calls != 2 {
		t.Fatal("caller quota bypass")
	}
}

func TestCallerManagementHTTPConfirmationScopeAndRevocation(t *testing.T) {
	s, _ := testServer(t)
	raw, csrf := login(t, s, "owner")
	body := map[string]any{"name": "integration", "wakers": []string{"w1"}, "quota": 10, "days": 2}
	if request(t, s, "POST", "/api/callers/create", raw, csrf, body).Code != 409 {
		t.Fatal("caller creation missing confirmation")
	}
	body["confirm"] = "/api/callers/create"
	body["wakers"] = []string{"other"}
	if request(t, s, "POST", "/api/callers/create", raw, csrf, body).Code != 400 {
		t.Fatal("unknown Waker authorized")
	}
	body["wakers"] = []string{"w1"}
	response := request(t, s, "POST", "/api/callers/create", raw, csrf, body)
	var result struct{ ID, Token string }
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &result) != nil || result.ID == "" || result.Token == "" {
		t.Fatal("caller creation failed", response.Code)
	}
	listing := request(t, s, "GET", "/api/callers", raw, csrf, nil)
	if strings.Contains(listing.Body.String(), result.Token) {
		t.Fatal("caller list returned raw token")
	}
	if request(t, s, "POST", "/api/callers/revoke", raw, csrf, map[string]any{"id": result.ID, "confirm": "/api/callers/revoke"}).Code != 200 {
		t.Fatal("caller revoke failed")
	}
	if request(t, s, "POST", "/api/gw", "", "", map[string]any{"wakerId": "w1", "message": "hello"}, map[string]string{"Authorization": "Bearer " + result.Token}).Code != 401 {
		t.Fatal("revoked caller was accepted")
	}
}
