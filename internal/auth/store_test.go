package auth

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"qoderwake-panel/internal/apierror"
	"sync"
	"testing"
	"time"
)

const testPassword = "example-password-for-tests"

func TestLegacyIdentityRefreshAfterAccountsEnabled(t *testing.T) {
	s := openTest(t)
	for _, role := range []string{"admin", "viewer"} {
		if _, err := s.Refresh(Identity{Role: role}); err != nil {
			t.Fatal("legacy identity rejected before migration", err)
		}
	}
	if _, err := s.CreateUser("owner", "Owner", "admin", nil, testPassword, true); err != nil {
		t.Fatal(err)
	}
	for _, role := range []string{"admin", "viewer"} {
		_, err := s.Refresh(Identity{Role: role})
		requireCode(t, err, "session_expired")
	}
}

func openTest(t *testing.T) *Store {
	t.Helper()
	s, e := Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.Close() })
	return s
}
func requireCode(t *testing.T, err error, code string) {
	t.Helper()
	var e *apierror.Error
	if !errors.As(err, &e) || e.Code != code {
		t.Fatalf("expected %s, got %v", code, err)
	}
}
func cookieRequest(raw string) *httpRequestHolder { return &httpRequestHolder{raw} }

type httpRequestHolder struct{ raw string }

func (h *httpRequestHolder) auth(s *Store) (Identity, error) {
	r := httptest.NewRequest("GET", "http://localhost/api/whoami", nil)
	r.Header.Set("Cookie", Cookie+"="+h.raw)
	return s.Authenticate(r)
}
func TestLegacyPythonAccountAndSessionCompatibility(t *testing.T) {
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("Python reference runtime unavailable")
	}
	root := t.TempDir()
	script := `import json,sys
from pathlib import Path
sys.path.insert(0,sys.argv[2])
from panel_security import Security
s=Security(Path(sys.argv[1]))
u=s.bootstrap_admin('owner','Owner','example-password-for-tests')
m=s.create_user('editor','Editor','example-password-for-tests','maintainer',['w1'])
raw,identity=s.login_user('editor','example-password-for-tests')
print(json.dumps({'id':m['id'],'raw':raw,'csrf':identity['csrf']}))`
	reference, err := filepath.Abs("../../panel")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(py, "-c", script, root, reference)
	cmd.Env = append(os.Environ(), "PYTHONDONTWRITEBYTECODE=1")
	b, err := cmd.Output()
	if err != nil {
		t.Fatalf("reference fixture failed: %v", err)
	}
	var fixture struct{ ID, Raw, CSRF string }
	if err = json.Unmarshal(b, &fixture); err != nil {
		t.Fatal(err)
	}
	s, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	identity, err := cookieRequest(fixture.Raw).auth(s)
	if err != nil {
		t.Fatal(err)
	}
	if identity.UserID != fixture.ID || identity.CSRF != fixture.CSRF || identity.Role != "maintainer" {
		t.Fatal("reference identity mismatch")
	}
	_, id, err := s.LoginUser("EDITOR", testPassword)
	if err != nil || id.UserID != fixture.ID {
		t.Fatal("reference password/login incompatible", err)
	}
	if err = Authorize(identity, "POST", "/api/skill/update", map[string]any{"wakerId": "w1"}); err != nil {
		t.Fatal(err)
	}
	requireCode(t, Authorize(identity, "POST", "/api/skill/update", map[string]any{"wakerId": "w2"}), "waker_scope_rejected")
	if _, err = s.UpdateUser(id.UserID, "Observer", "observer", []string{"w1"}, true, id.Revision); err != nil {
		t.Fatal(err)
	}
	_, err = cookieRequest(fixture.Raw).auth(s)
	requireCode(t, err, "session_expired")
	// Python also accepts a Go-created password, scope and session on the same schema.
	raw, identity, err := s.LoginUser("editor", testPassword)
	if err != nil {
		t.Fatal(err)
	}
	cmd = exec.Command(py, "-c", `import sys,json
from pathlib import Path
sys.path.insert(0,sys.argv[2])
from panel_security import Security
s=Security(Path(sys.argv[1]))
x=s.authenticate({'Cookie':'qwp_session='+sys.stdin.read()})
print(json.dumps({'role':x['role'],'wakers':x['wakers']}))`, root, reference)
	cmd.Env = append(os.Environ(), "PYTHONDONTWRITEBYTECODE=1")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	go func() { defer stdin.Close(); stdin.Write([]byte(raw)) }()
	b, err = cmd.Output()
	if err != nil {
		t.Fatalf("reference readback failed: %v", err)
	}
	var out struct {
		Role   string
		Wakers []string
	}
	if err = json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if out.Role != "observer" || len(out.Wakers) != 1 || out.Wakers[0] != "w1" {
		t.Fatal("reference readback mismatch")
	}
}
func TestBootstrapRevocationAndLastAdministrator(t *testing.T) {
	s := openTest(t)
	raw, _, e := s.LoginToken(s.admin)
	if e != nil {
		t.Fatal(e)
	}
	u, e := s.CreateUser("owner", "Owner", "admin", nil, testPassword, true)
	if e != nil {
		t.Fatal(e)
	}
	_, e = cookieRequest(raw).auth(s)
	requireCode(t, e, "session_expired")
	if s.RoleFor(s.admin) != "" {
		t.Fatal("legacy bearer remains enabled")
	}
	_, e = s.UpdateUser(u.ID, "Owner", "observer", nil, true, u.Revision)
	requireCode(t, e, "last_admin_required")
	second, e := s.CreateUser("other", "Other", "admin", nil, testPassword, false)
	if e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, user := range []User{u, second} {
		wg.Add(1)
		go func(user User) {
			defer wg.Done()
			_, e := s.UpdateUser(user.ID, user.DisplayName, "observer", nil, true, user.Revision)
			errs <- e
		}(user)
	}
	wg.Wait()
	close(errs)
	successes := 0
	for e := range errs {
		if e == nil {
			successes++
		} else {
			requireCode(t, e, "last_admin_required")
		}
	}
	if successes != 1 {
		t.Fatal("last-admin concurrency invariant")
	}
}
func TestPasswordChangeAndExpiry(t *testing.T) {
	s := openTest(t)
	_, e := s.CreateUser("owner", "Owner", "admin", nil, testPassword, true)
	if e != nil {
		t.Fatal(e)
	}
	raw, id, e := s.LoginUser("owner", testPassword)
	if e != nil {
		t.Fatal(e)
	}
	requireCode(t, s.ChangePassword(id, "wrong", testPassword), "current_password_invalid")
	if e = s.ChangePassword(id, testPassword, "another-example-password"); e != nil {
		t.Fatal(e)
	}
	_, e = cookieRequest(raw).auth(s)
	requireCode(t, e, "session_expired")
	raw, _, e = s.LoginUser("owner", "another-example-password")
	if e != nil {
		t.Fatal(e)
	}
	s.now = func() time.Time { return time.Now().Add(13 * time.Hour) }
	_, e = cookieRequest(raw).auth(s)
	requireCode(t, e, "session_expired")
}
func TestCallerQuotaAndAuditScope(t *testing.T) {
	s := openTest(t)
	caller, e := s.CreateCaller("tests", []string{"w1"}, 1, 1)
	if e != nil {
		t.Fatal(e)
	}
	r := httptest.NewRequest("POST", "http://localhost/api/gw", nil)
	r.Header.Set("Authorization", "Bearer "+caller["token"])
	id, e := s.Authenticate(r)
	if e != nil {
		t.Fatal(e)
	}
	requireCode(t, s.ConsumeCaller(id, "w2"), "waker_scope_rejected")
	if e = s.ConsumeCaller(id, "w1"); e != nil {
		t.Fatal(e)
	}
	requireCode(t, s.ConsumeCaller(id, "w1"), "caller_quota_exhausted")
	for _, w := range []string{"w1", "w2"} {
		if e = s.Audit("127.0.0.1", id, "test", w, 200, true, w, "v1", "v2"); e != nil {
			t.Fatal(e)
		}
	}
	rows, e := s.Recent(Identity{UserID: "observer", Role: "observer", Wakers: []string{"w1"}})
	if e != nil {
		t.Fatal(e)
	}
	if len(rows) != 1 || rows[0]["wakerId"] != "w1" {
		t.Fatal("audit scope mismatch")
	}
	if _, ok := rows[0]["ip"]; ok {
		t.Fatal("restricted audit exposes IP")
	}
	if e = s.RevokeCaller(id.CallerID); e != nil {
		t.Fatal(e)
	}
	_, e = s.Authenticate(r)
	requireCode(t, e, "invalid_credentials")
}
func TestRevisionAndRateLimit(t *testing.T) {
	s := openTest(t)
	u, e := s.CreateUser("owner", "Owner", "admin", nil, testPassword, true)
	if e != nil {
		t.Fatal(e)
	}
	_, e = s.UpdateUser(u.ID, "Changed", "admin", nil, true, 99)
	requireCode(t, e, "version_conflict")
	for i := 0; i < 3; i++ {
		ok, e := s.Limit("example", 2, time.Minute)
		if e != nil {
			t.Fatal(e)
		}
		if ok != (i < 2) {
			t.Fatal("rate limit mismatch")
		}
	}
}

func TestUnavailableAccountsStoreCannotEnableLegacyAccess(t *testing.T) {
	s := openTest(t)
	if _, e := s.CreateUser("owner", "Owner", "admin", nil, testPassword, true); e != nil {
		t.Fatal(e)
	}
	if e := s.DB.Close(); e != nil {
		t.Fatal(e)
	}
	if s.RoleFor(s.admin) != "" {
		t.Fatal("database failure re-enabled legacy admin")
	}
	r := httptest.NewRequest("GET", "http://localhost/api/state", nil)
	r.Header.Set("Authorization", "Bearer "+s.admin)
	if _, e := s.Authenticate(r); e == nil {
		t.Fatal("database failure authenticated legacy admin")
	}
}
