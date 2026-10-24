package daemon

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"qoderwake-panel/internal/apierror"
)

func newSession(t *testing.T, handler http.HandlerFunc) *Session {
	t.Helper()
	upstream := httptest.NewServer(handler)
	t.Cleanup(upstream.Close)
	root := t.TempDir()
	token := filepath.Join(root, "token")
	if e := os.WriteFile(token, []byte(strings.Repeat("a", 64)), 0600); e != nil {
		t.Fatal(e)
	}
	s, e := New(Config{URL: upstream.URL, TokenPath: token, Root: root})
	if e != nil {
		t.Fatal(e)
	}
	return s
}
func bootstrap(w http.ResponseWriter, r *http.Request) bool {
	if r.URL.Path != "/api/frontend-auth/session/bootstrap" {
		return false
	}
	if r.Header.Get("X-QoderWake-Frontend-Session-Client") != "cli" || !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
		http.Error(w, "bad bootstrap", 400)
		return true
	}
	http.SetCookie(w, &http.Cookie{Name: frontendCookie, Value: "synthetic-session", Path: "/"})
	w.Write([]byte(`{"ok":true}`))
	return true
}
func TestReadRetriesAuthenticationOnce(t *testing.T) {
	var boots, reads atomic.Int32
	s := newSession(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/frontend-auth/session/bootstrap" {
			boots.Add(1)
			bootstrap(w, r)
			return
		}
		if reads.Add(1) == 1 {
			http.Error(w, `{"code":"expired"}`, 401)
			return
		}
		if cookie, e := r.Cookie(frontendCookie); e != nil || cookie.Value != "synthetic-session" {
			t.Error("missing frontend cookie")
		}
		w.Write([]byte(`{"data":{"value":"ok"}}`))
	})
	r, e := s.JSON(context.Background(), "GET", "/api/example", nil)
	if e != nil {
		t.Fatal(e)
	}
	if Data(r)["value"] != "ok" || reads.Load() != 2 || boots.Load() != 2 {
		t.Fatal("read retry contract mismatch")
	}
}
func TestWriteIsNotReplayed(t *testing.T) {
	var writes atomic.Int32
	s := newSession(t, func(w http.ResponseWriter, r *http.Request) {
		if bootstrap(w, r) {
			return
		}
		writes.Add(1)
		http.Error(w, `{}`, 401)
	})
	_, e := s.JSON(context.Background(), "POST", "/api/example", map[string]any{})
	var de *Error
	if !errors.As(e, &de) || de.Status != 403 || writes.Load() != 1 {
		t.Fatal("write replay or wrong status", e)
	}
}
func TestPendingAndConflictPreserveMeaning(t *testing.T) {
	for _, status := range []int{202, 409} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			s := newSession(t, func(w http.ResponseWriter, r *http.Request) {
				if bootstrap(w, r) {
					return
				}
				w.WriteHeader(status)
				w.Write([]byte(`{"code":"pending_write","operationId":"operation-example","secret":"must-not-be-forwarded","data":{"status":"pending"}}`))
			})
			_, e := s.JSON(context.Background(), "POST", "/api/example", map[string]any{})
			if status == 409 {
				var ae *apierror.Error
				if !errors.As(e, &ae) || ae.Code != "version_conflict" {
					t.Fatal(e)
				}
			} else {
				var de *Error
				if !errors.As(e, &de) || de.Status != 202 || de.Detail["operationId"] != "operation-example" {
					t.Fatal(e)
				}
				if _, ok := de.Detail["secret"]; ok {
					t.Fatal("unapproved error detail exposed")
				}
			}
		})
	}
}
func TestOriginRedirectAndSizeBoundaries(t *testing.T) {
	var hits atomic.Int32
	s := newSession(t, func(w http.ResponseWriter, r *http.Request) {
		if bootstrap(w, r) {
			return
		}
		hits.Add(1)
		if r.URL.Path == "/api/large" {
			w.Write([]byte(strings.Repeat("x", MaxResponse+1)))
			return
		}
		w.Header().Set("Location", "http://example.invalid/")
		w.WriteHeader(302)
	})
	_, _, _, e := s.Raw(context.Background(), "GET", "//example.invalid/api", nil, MaxResponse)
	var ae *apierror.Error
	if !errors.As(e, &ae) || hits.Load() != 0 {
		t.Fatal("origin accepted", e)
	}
	_, e = s.JSON(context.Background(), "GET", "/api/redirect", nil)
	if e == nil || hits.Load() != 1 {
		t.Fatal("redirect unexpectedly followed")
	}
	_, e = s.JSON(context.Background(), "GET", "/api/large", nil)
	if !errors.As(e, &ae) || ae.Code != "daemon_response_too_large" {
		t.Fatal("response size was not bounded", e)
	}
}
func TestCredentialHelperMatchesReference(t *testing.T) {
	reference, e := os.ReadFile("../../panel/native-credential.mjs")
	if e != nil {
		t.Fatal(e)
	}
	if string(reference) != string(credentialHelper) {
		t.Fatal("credential helper drifted from reference")
	}
}
