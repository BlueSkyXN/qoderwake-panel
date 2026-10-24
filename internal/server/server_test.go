package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"qoderwake-panel/internal/webui"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"

	"qoderwake-panel/internal/apierror"
	"qoderwake-panel/internal/auth"
)

type fakeDaemon struct {
	mu               sync.Mutex
	content, version string
	writes           int
}

func TestLegacyCookieMigrationAndAccountsCutover(t *testing.T) {
	root := t.TempDir()
	s, err := New(Config{Root: root, Home: filepath.Join(root, "home"), DaemonURL: "http://127.0.0.1:1"})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	raw, err := os.ReadFile(filepath.Join(root, "admin-token.txt"))
	if err != nil {
		t.Fatal(err)
	}
	header := map[string]string{"Cookie": "qwtk=" + strings.TrimSpace(string(raw))}
	w := request(t, s, "POST", "/api/migrate", "", "", map[string]any{}, header)
	if w.Code != 200 {
		t.Fatal("legacy cookie rejected", w.Code)
	}
	var migrated string
	for _, cookie := range w.Result().Cookies() {
		if cookie.Name == auth.Cookie {
			migrated = cookie.Value
		}
	}
	if migrated == "" {
		t.Fatal("session cookie missing")
	}
	if w := request(t, s, "GET", "/api/whoami", migrated, "", nil); w.Code != 200 {
		t.Fatal("migrated session invalid", w.Code)
	}
	if _, err := s.Auth.CreateUser("owner", "Owner", "admin", nil, "example-password-for-tests", true); err != nil {
		t.Fatal(err)
	}
	if w := request(t, s, "POST", "/api/migrate", "", "", map[string]any{}, header); w.Code != 401 {
		t.Fatal("legacy cookie remained valid after accounts enabled", w.Code)
	}
}

func (f *fakeDaemon) JSON(ctx context.Context, method, path string, b any) (map[string]any, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	skill := map[string]any{"skillId": "s1", "name": "演示技能", "enabled": true, "mutableByAgent": true, "pinned": false, "currentVersionId": f.version, "internalSecret": "synthetic-sensitive-metadata"}
	if path == "/api/agents/w1" {
		return map[string]any{"data": map[string]any{"agentId": "w1", "name": "Waker", "description": "示例配置", "skills": []any{skill}}}, nil
	}
	if path == "/api/agents/w1/skills/s1/content" {
		if method == "PUT" {
			body := b.(map[string]any)
			if body["baseVersionId"] != f.version {
				return nil, apierror.New(409, "version_conflict")
			}
			f.writes++
			f.content = body["content"].(string)
			f.version = "v" + strconv.Itoa(f.writes+1)
		}
		return map[string]any{"data": map[string]any{"skill": skill, "content": f.content}}, nil
	}
	return nil, apierror.New(404, "daemon_resource_not_found")
}
func testServer(t *testing.T) (*Server, *fakeDaemon) {
	t.Helper()
	root := t.TempDir()
	s, e := New(Config{Root: root, Home: filepath.Join(root, "daemon"), DaemonURL: "http://127.0.0.1:1"})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.Close() })
	if _, e = s.Auth.CreateUser("owner", "Owner", "admin", nil, "example-password-for-tests", true); e != nil {
		t.Fatal(e)
	}
	fake := &fakeDaemon{content: "original\n", version: "v1"}
	s.Daemon = fake
	return s, fake
}
func request(t *testing.T, s *Server, method, path, raw, csrf string, body any, headers ...map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	var payload []byte
	if body != nil {
		var e error
		payload, e = json.Marshal(body)
		if e != nil {
			t.Fatal(e)
		}
	}
	r := httptest.NewRequest(method, "http://localhost"+path, bytes.NewReader(payload))
	if raw != "" {
		r.Header.Set("Cookie", auth.Cookie+"="+raw)
	}
	if method == "POST" {
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", "http://localhost")
		r.Header.Set("X-CSRF-Token", csrf)
	}
	for _, values := range headers {
		for key, value := range values {
			r.Header.Set(key, value)
		}
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}
func login(t *testing.T, s *Server, username string) (string, string) {
	t.Helper()
	w := request(t, s, "POST", "/api/login", "", "", map[string]any{"username": username, "password": "example-password-for-tests"})
	if w.Code != 200 {
		t.Fatal("login failed", w.Code)
	}
	var data struct{ CSRF string }
	if e := json.Unmarshal(w.Body.Bytes(), &data); e != nil {
		t.Fatal(e)
	}
	return w.Result().Cookies()[0].Value, data.CSRF
}
func TestAccountsCSRFRoleAndResourceScope(t *testing.T) {
	s, _ := testServer(t)
	for _, role := range []string{"maintainer", "observer"} {
		if _, e := s.Auth.CreateUser(role, role, role, []string{"w1"}, "example-password-for-tests", false); e != nil {
			t.Fatal(e)
		}
	}
	if w := request(t, s, "GET", "/api/state", "", "", nil); w.Code != 401 {
		t.Fatal("anonymous allowed")
	}
	raw, csrf := login(t, s, "maintainer")
	if w := request(t, s, "GET", "/api/skills?waker=w2", raw, csrf, nil); w.Code != 403 {
		t.Fatal("scope escape")
	}
	if w := request(t, s, "GET", "/api/users", raw, csrf, nil); w.Code != 403 {
		t.Fatal("role escape")
	}
	body := map[string]any{"wakerId": "w1", "skillId": "s1", "baseVersionId": "v1", "content": "changed", "confirm": "/api/skill/update"}
	if w := request(t, s, "POST", "/api/skill/update", raw, "bad", body); w.Code != 403 {
		t.Fatal("CSRF bypass")
	}
	observer, token := login(t, s, "observer")
	if w := request(t, s, "POST", "/api/skill/update", observer, token, body); w.Code != 403 {
		t.Fatal("observer mutation")
	}
	if w := request(t, s, "GET", "/api/skill/content?waker=w1&skill=s1", observer, token, nil); w.Code != 200 || !strings.Contains(w.Body.String(), `"editable":false`) {
		t.Fatal("observer editability mismatch")
	}
}
func TestTwoAccountsConflictAndRevocation(t *testing.T) {
	s, fake := testServer(t)
	user, e := s.Auth.CreateUser("editor", "Editor", "maintainer", []string{"w1"}, "example-password-for-tests", false)
	if e != nil {
		t.Fatal(e)
	}
	owner, ownerCSRF := login(t, s, "owner")
	editor, editorCSRF := login(t, s, "editor")
	body := map[string]any{"wakerId": "w1", "skillId": "s1", "baseVersionId": "v1", "content": "  edited\n\n", "confirm": "/api/skill/update"}
	if w := request(t, s, "POST", "/api/skill/update", owner, ownerCSRF, body); w.Code != 200 {
		t.Fatal("first save failed", w.Code)
	}
	if w := request(t, s, "POST", "/api/skill/update", editor, editorCSRF, body); w.Code != 409 {
		t.Fatal("stale version accepted", w.Code)
	}
	if fake.writes != 1 || fake.content != "  edited\n\n" {
		t.Fatal("write was replayed or whitespace changed")
	}
	_, e = s.Auth.UpdateUser(user.ID, "Editor", "observer", []string{"w1"}, true, user.Revision)
	if e != nil {
		t.Fatal(e)
	}
	if w := request(t, s, "GET", "/api/skills?waker=w1", editor, editorCSRF, nil); w.Code != 401 {
		t.Fatal("revoked session accepted")
	}
}
func TestSkillLimitUsesUTF8Bytes(t *testing.T) {
	s, fake := testServer(t)
	raw, csrf := login(t, s, "owner")
	w := request(t, s, "POST", "/api/skill/update", raw, csrf, map[string]any{"wakerId": "w1", "skillId": "s1", "baseVersionId": "v1", "content": strings.Repeat("界", 20001), "confirm": "/api/skill/update"})
	if w.Code != 400 || fake.writes != 0 {
		t.Fatal("skill byte limit mismatch", w.Code)
	}
}
func TestWakerRevisionMatchesPython(t *testing.T) {
	py, e := exec.LookPath("python3")
	if e != nil {
		t.Skip("reference Python unavailable")
	}
	value := map[string]any{"name": "中文 <&>", "description": "line\n内容", "skills": []any{map[string]any{"skillId": "s2", "enabled": false}, map[string]any{"skillId": "s1", "enabled": true}}}
	raw, _ := json.Marshal(value)
	cmd := exec.Command(py, "-c", `import json,sys,hashlib
x=json.load(sys.stdin)
x['skills']=sorted(x['skills'],key=lambda s:str(s['skillId']))
print(hashlib.sha256(json.dumps(x,sort_keys=True,ensure_ascii=False).encode()).hexdigest())`)
	cmd.Stdin = bytes.NewReader(raw)
	out, e := cmd.Output()
	if e != nil {
		t.Fatal(e)
	}
	if wakerRevision(value) != strings.TrimSpace(string(out)) {
		t.Fatal("revision incompatible with Python")
	}
}

func TestEmbeddedFrontendContentTypes(t *testing.T) {
	s, _ := testServer(t)
	s.Config.Assets = webui.Handler()
	page := request(t, s, "GET", "/", "", "", nil)
	if page.Code == 503 {
		t.Skip("frontend build has not been generated")
	}
	if page.Code != 200 || !strings.HasPrefix(page.Header().Get("Content-Type"), "text/html") {
		t.Fatal("HTML content type mismatch")
	}
	pattern := regexp.MustCompile(`src="(/assets/[^"]+\.js)"`)
	match := pattern.FindStringSubmatch(page.Body.String())
	if len(match) != 2 {
		t.Fatal("frontend module missing")
	}
	asset := request(t, s, "GET", match[1], "", "", nil)
	if asset.Code != 200 || !strings.Contains(asset.Header().Get("Content-Type"), "javascript") {
		t.Fatal("module content type mismatch")
	}
}

func TestMaintenanceBlocksMutationsAndAllowsExit(t *testing.T) {
	s, fake := testServer(t)
	raw, csrf := login(t, s, "owner")
	enter := request(t, s, "POST", "/api/maintenance/enter", raw, csrf, map[string]any{"reason": "fixture maintenance", "ttl": 30, "confirm": "/api/maintenance/enter"})
	if enter.Code != 200 || s.Admission.Status().Mode != "maintenance" {
		t.Fatal("maintenance not entered", enter.Code)
	}
	write := request(t, s, "POST", "/api/skill/update", raw, csrf, map[string]any{"wakerId": "w1", "skillId": "s1", "baseVersionId": "v1", "content": "blocked", "confirm": "/api/skill/update"})
	if write.Code != 503 || fake.writes != 0 {
		t.Fatal("maintenance write admitted", write.Code)
	}
	read := request(t, s, "GET", "/api/skills?waker=w1", raw, csrf, nil)
	if read.Code != 200 {
		t.Fatal("maintenance read rejected", read.Code)
	}
	exit := request(t, s, "POST", "/api/maintenance/exit", raw, csrf, map[string]any{})
	if exit.Code != 200 || s.Admission.Status().Mode != "normal" {
		t.Fatal("maintenance exit failed", exit.Code)
	}
}

func TestRestrictedWakerMetadataIsFiltered(t *testing.T) {
	s, _ := testServer(t)
	_, err := s.Auth.CreateUser("observer", "Observer", "observer", []string{"w1"}, "example-password-for-tests", false)
	if err != nil {
		t.Fatal(err)
	}
	raw, csrf := login(t, s, "observer")
	w := request(t, s, "GET", "/api/waker/detail?id=w1", raw, csrf, nil)
	if w.Code != 200 || strings.Contains(w.Body.String(), "synthetic-sensitive-metadata") {
		t.Fatal("restricted metadata boundary failed", w.Code)
	}
}
