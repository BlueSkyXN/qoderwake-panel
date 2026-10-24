// Package daemon implements the official CLI frontend-session HTTP contract.
package daemon

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"qoderwake-panel/internal/apierror"
)

//go:embed native-credential.mjs
var credentialHelper []byte

const MaxResponse = 1024 * 1024
const frontendCookie = "qoderwake_frontend_session"

type Config struct{ URL, TokenPath, CLI, CredentialService, Root string }
type Session struct {
	cfg        Config
	base       *url.URL
	mu         sync.Mutex
	client     *http.Client
	generation uint64
}
type Error struct {
	Status int
	Code   string
	Detail map[string]any
}

func (e *Error) Error() string { return e.Code }

type Conflict struct {
	Code   string
	Detail map[string]any
}

func (e *Conflict) Error() string { return e.Code }
func (e *Conflict) Unwrap() error { return apierror.New(409, "version_conflict") }
func New(cfg Config) (*Session, error) {
	base, err := url.Parse(cfg.URL)
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Hostname() == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" || strings.Trim(base.Path, "/") != "" {
		return nil, apierror.Invalid("invalid_daemon_url")
	}
	if cfg.CredentialService == "" {
		cfg.CredentialService = "qoderwake.auth.v1"
	}
	return &Session{cfg: cfg, base: base}, nil
}
func readToken(path string) (string, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return "", apierror.New(503, "daemon_frontend_token_unavailable")
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return "", err
	}
	info, ok := st.Sys().(*syscall.Stat_t)
	if !ok || !st.Mode().IsRegular() || st.Size() > 4096 || st.Mode().Perm()&0077 != 0 || int(info.Uid) != os.Geteuid() {
		return "", apierror.New(503, "daemon_frontend_token_unavailable")
	}
	b, err := io.ReadAll(io.LimitReader(f, 4097))
	if err != nil {
		return "", err
	}
	s := strings.TrimSpace(string(b))
	if len(s) < 16 || len(s) > 4096 || strings.ContainsAny(s, "\r\n") {
		return "", apierror.New(503, "daemon_frontend_token_unavailable")
	}
	return s, nil
}

type boundedBuffer struct {
	bytes.Buffer
	limit int
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > b.limit {
		return 0, errors.New("output_limit")
	}
	return b.Buffer.Write(p)
}
func (s *Session) nativeToken(ctx context.Context, stored string) (string, error) {
	if !strings.HasPrefix(stored, "os-credential-v1:") {
		return "", apierror.New(503, "daemon_credential_format_unsupported")
	}
	if s.cfg.CLI == "" {
		return "", apierror.New(503, "daemon_credential_runtime_unavailable")
	}
	dir, err := os.MkdirTemp(s.cfg.Root, ".credential-helper-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	helper := filepath.Join(dir, "native-credential.mjs")
	if err = os.WriteFile(helper, credentialHelper, 0600); err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, s.cfg.CLI, helper, s.cfg.TokenPath, s.cfg.CredentialService)
	cmd.Env = append(os.Environ(), "BUN_BE_BUN=1")
	stdout := &boundedBuffer{limit: 8192}
	stderr := &boundedBuffer{limit: 4096}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err = cmd.Run(); err != nil {
		code := strings.TrimSpace(stderr.String())
		switch code {
		case "credential_runtime_unavailable", "credential_file_unavailable", "credential_format_unsupported", "credential_store_unavailable", "credential_decryption_failed":
			return "", apierror.New(503, "daemon_"+code)
		}
		return "", apierror.New(503, "daemon_credential_runtime_unavailable")
	}
	var result struct{ Token string }
	if json.Unmarshal(stdout.Bytes(), &result) != nil || len(result.Token) < 16 || len(result.Token) > 4096 {
		return "", apierror.New(503, "daemon_credential_decryption_failed")
	}
	for _, c := range result.Token {
		if c < 33 || c > 126 {
			return "", apierror.New(503, "daemon_credential_decryption_failed")
		}
	}
	return result.Token, nil
}
func (s *Session) ensure(ctx context.Context) (*http.Client, uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.client != nil {
		return s.client, s.generation, nil
	}
	token, err := readToken(s.cfg.TokenPath)
	if err != nil {
		return nil, 0, err
	}
	if strings.HasPrefix(token, "os-credential-") {
		token, err = s.nativeToken(ctx, token)
		if err != nil {
			return nil, 0, err
		}
	}
	jar, _ := cookiejar.New(nil)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	client := &http.Client{Jar: jar, Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(s.cfg.URL, "/")+"/api/frontend-auth/session/bootstrap", strings.NewReader("{}"))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-QoderWake-Frontend-Session-Client", "cli")
	res, err := client.Do(req)
	if err != nil {
		transport.CloseIdleConnections()
		return nil, 0, apierror.New(502, "daemon_unavailable")
	}
	defer res.Body.Close()
	if _, err = readBounded(res.Body, MaxResponse); err != nil {
		transport.CloseIdleConnections()
		return nil, 0, err
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		transport.CloseIdleConnections()
		if res.StatusCode == 401 || res.StatusCode == 403 {
			return nil, 0, apierror.New(403, "daemon_frontend_bootstrap_rejected")
		}
		return nil, 0, apierror.New(502, "daemon_frontend_bootstrap_failed")
	}
	found := false
	for _, cookie := range jar.Cookies(s.base) {
		found = found || cookie.Name == frontendCookie
	}
	if !found {
		transport.CloseIdleConnections()
		return nil, 0, apierror.New(502, "daemon_frontend_session_missing")
	}
	s.client = client
	s.generation++
	return client, s.generation, nil
}
func readBounded(r io.Reader, limit int64) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, apierror.New(502, "daemon_response_too_large")
	}
	return b, nil
}
func (s *Session) invalidate(gen uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if gen == s.generation {
		if s.client != nil {
			s.client.CloseIdleConnections()
		}
		s.client = nil
	}
}
func (s *Session) Raw(ctx context.Context, method, path string, body any, limit int64) ([]byte, int, http.Header, error) {
	relative, err := url.ParseRequestURI(path)
	if err != nil || !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") || relative.IsAbs() {
		return nil, 0, nil, apierror.New(500, "daemon_request_origin_rejected")
	}
	target := s.base.ResolveReference(relative)
	if target.Host != s.base.Host || target.Scheme != s.base.Scheme {
		return nil, 0, nil, apierror.New(500, "daemon_request_origin_rejected")
	}
	var payload []byte
	if body != nil {
		payload, err = json.Marshal(body)
		if err != nil {
			return nil, 0, nil, err
		}
	}
	for attempt := 0; attempt < 2; attempt++ {
		client, gen, e := s.ensure(ctx)
		if e != nil {
			return nil, 0, nil, e
		}
		req, e := http.NewRequestWithContext(ctx, method, target.String(), bytes.NewReader(payload))
		if e != nil {
			return nil, 0, nil, e
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", "QoderWake-Panel")
		res, e := client.Do(req)
		if e != nil {
			return nil, 0, nil, apierror.New(502, "daemon_unavailable")
		}
		if res.StatusCode == 401 && attempt == 0 && (method == "GET" || method == "HEAD") {
			res.Body.Close()
			s.invalidate(gen)
			continue
		}
		b, e := readBounded(res.Body, limit)
		res.Body.Close()
		return b, res.StatusCode, res.Header, e
	}
	return nil, 0, nil, apierror.New(502, "daemon_unavailable")
}
func Data(value map[string]any) map[string]any {
	if data, ok := value["data"].(map[string]any); ok {
		return data
	}
	if _, ok := value["data"]; ok {
		return map[string]any{}
	}
	return value
}
func Detail(value map[string]any) map[string]any {
	out := map[string]any{}
	data, _ := value["data"].(map[string]any)
	failure, _ := value["error"].(map[string]any)
	for _, key := range []string{"code", "operationId", "pendingId", "status", "retryable", "currentRevision"} {
		v, ok := value[key]
		if !ok {
			v, ok = data[key]
		}
		if !ok {
			v = failure[key]
		}
		switch v := v.(type) {
		case string:
			if len(v) <= 256 {
				out[key] = v
			}
		case bool:
			out[key] = v
		case json.Number:
			if _, e := v.Int64(); e == nil {
				out[key] = v
			}
		}
	}
	return out
}
func RequestError(status int, detail map[string]any) error {
	code, _ := detail["code"].(string)
	if status == 409 {
		if code == "" {
			code = "daemon_conflict"
		}
		return &Conflict{code, detail}
	}
	target := 502
	fallback := "daemon_request_failed"
	switch status {
	case 404:
		target = 404
		fallback = "daemon_resource_not_found"
	case 401:
		target = 403
		fallback = "daemon_owner_login_required"
	case 403:
		target = 403
		fallback = "daemon_permission_denied"
	case 429:
		target = 429
		fallback = "daemon_rate_limited"
	case 202:
		target = 202
		fallback = "daemon_operation_pending"
	case 400, 422:
		target = 400
		fallback = "daemon_request_rejected"
		if state, _ := detail["status"].(string); state == "pending" || state == "running" || state == "accepted" {
			target = 202
			fallback = "daemon_operation_pending"
		}
	}
	if code == "" {
		code = fallback
	}
	return &Error{target, code, detail}
}
func (s *Session) JSON(ctx context.Context, method, path string, body any) (map[string]any, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	raw, status, _, err := s.Raw(ctx, method, path, body, MaxResponse)
	if err != nil {
		return nil, err
	}
	var result map[string]any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err = decoder.Decode(&result); err != nil || result == nil || !utf8.Valid(raw) || decoder.Decode(new(any)) != io.EOF {
		if status < 200 || status >= 300 {
			return nil, RequestError(status, nil)
		}
		return nil, apierror.New(502, "daemon_response_invalid")
	}
	if status == 202 || status < 200 || status >= 300 {
		return nil, RequestError(status, Detail(result))
	}
	if result["success"] == false {
		return nil, RequestError(400, Detail(result))
	}
	return result, nil
}
