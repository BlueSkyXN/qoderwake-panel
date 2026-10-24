// Package server hosts the Panel API and its embedded React application.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"qoderwake-panel/internal/admission"
	"qoderwake-panel/internal/apierror"
	"qoderwake-panel/internal/auth"
	"qoderwake-panel/internal/cli"
	"qoderwake-panel/internal/daemon"
	"qoderwake-panel/internal/deletion"
	"qoderwake-panel/internal/patches"
	"qoderwake-panel/internal/provider"
	"qoderwake-panel/internal/usage"
)

const Version = "0.13.0-rc.1"

type Config struct {
	GatewayBinary, GatewayConfig, GatewayUpstream, GatewayUser                             string
	GatewayPort                                                                            int
	Root, Home, DaemonURL, CLI, TokenPath, CredentialService, PublicOrigin, AllowedClients string
	RequireTLS, PluginWrites                                                               bool
	Assets                                                                                 http.Handler
	Whitelist                                                                              string
}
type Daemon interface {
	JSON(context.Context, string, string, any) (map[string]any, error)
}
type Server struct {
	Config      Config
	Auth        *auth.Store
	Daemon      Daemon
	resources   sync.Mutex
	Catalog     *cli.Catalog
	Jobs        *cli.Jobs
	Files       *cli.Files
	Admission   *admission.Controller
	Providers   *provider.Settings
	Probes      *provider.Plans
	Deletions   *deletion.Journal
	Usage       *usage.Store
	cached      stateCache
	operationMu sync.Mutex
	operations  sync.WaitGroup
	closing     bool
	fileChecks  fileChecks
	patchOnce   sync.Once
	patches     *patches.Manager
}

func New(cfg Config) (*Server, error) {
	var err error
	cfg.Root, err = filepath.Abs(cfg.Root)
	if err != nil {
		return nil, err
	}
	cfg.Home, err = filepath.Abs(cfg.Home)
	if err != nil {
		return nil, err
	}
	if cfg.DaemonURL == "" {
		port := 19820
		if b, e := os.ReadFile(filepath.Join(cfg.Home, "run/daemon.port")); e == nil && len(b) <= 32 {
			if n, e := strconv.Atoi(strings.TrimSpace(string(b))); e == nil && n > 0 && n < 65536 {
				port = n
			}
		}
		cfg.DaemonURL = "http://127.0.0.1:" + strconv.Itoa(port)
	}
	if cfg.CLI == "" {
		cfg.CLI = filepath.Join(cfg.Home, "qoderwake")
	}
	if cfg.TokenPath == "" {
		cfg.TokenPath = filepath.Join(cfg.Home, ".auth/token")
	}
	for _, cidr := range strings.Split(cfg.AllowedClients, ",") {
		if strings.TrimSpace(cidr) != "" {
			if _, err = parseNetwork(cidr); err != nil {
				return nil, apierror.Invalid("invalid_allowed_clients")
			}
		}
	}
	store, err := auth.Open(cfg.Root)
	if err != nil {
		return nil, err
	}
	transport, err := daemon.New(daemon.Config{URL: cfg.DaemonURL, Root: cfg.Root, TokenPath: cfg.TokenPath, CLI: cfg.CLI, CredentialService: cfg.CredentialService})
	if err != nil {
		store.Close()
		return nil, err
	}
	catalog, err := cli.NewCatalog()
	if err != nil {
		store.Close()
		return nil, err
	}
	jobs, err := cli.OpenJobs(filepath.Join(cfg.Root, "native-operations"), cfg.Home, cfg.CLI, catalog)
	if err != nil {
		store.Close()
		return nil, err
	}
	files, err := cli.OpenFiles(filepath.Join(cfg.Root, "native-files"))
	if err != nil {
		jobs.Close()
		store.Close()
		return nil, err
	}
	metrics, err := usage.Open(filepath.Join(cfg.Root, "usage.db"), filepath.Join(cfg.Home, "qodercli/logs/runs"))
	if err != nil {
		jobs.Close()
		store.Close()
		return nil, err
	}
	app := &Server{Config: cfg, Auth: store, Daemon: transport, Catalog: catalog, Jobs: jobs, Files: files, Admission: admission.New(), Usage: metrics}
	app.Providers = provider.NewSettings(filepath.Join(cfg.Home, "qodercli/settings.json"), filepath.Join(cfg.Root, "provider-settings.previous.json"))
	app.Probes = provider.NewPlans()
	app.Deletions = deletion.NewJournal(store.DB)
	jobs.Guard = app.nativeGuard
	jobs.Resolve = files.Resolve
	jobs.Audit = func(id auth.Identity, result cli.Result) {
		app.invalidateState()
		app.Usage.Invalidate()
		status := 502
		if result.Status == "succeeded" {
			status = 200
		} else if result.Status == "unknown" {
			status = 202
		}
		store.Audit("", id, "native:"+result.Operation, result.ID, status, result.Status == "succeeded", result.Waker, "", "")
	}
	metrics.Start()
	return app, nil
}
func (s *Server) Close() error {
	s.operationMu.Lock()
	s.closing = true
	s.operationMu.Unlock()
	jobs := s.Jobs.Close()
	s.operations.Wait()
	return errors.Join(jobs, s.Usage.Close(), s.Auth.Close())
}
func parseNetwork(raw string) (netip.Prefix, error) {
	raw = strings.TrimSpace(raw)
	if ip, err := netip.ParseAddr(raw); err == nil {
		return netip.PrefixFrom(ip, ip.BitLen()), nil
	}
	return netip.ParsePrefix(raw)
}
func (s *Server) transport(r *http.Request) error {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return apierror.New(403, "client_network_rejected")
	}
	peer, err := netip.ParseAddr(host)
	if err != nil {
		return apierror.New(403, "client_network_rejected")
	}
	peer = peer.Unmap()
	if s.Config.AllowedClients != "" {
		allowed := false
		for _, raw := range strings.Split(s.Config.AllowedClients, ",") {
			network, e := parseNetwork(raw)
			allowed = allowed || (e == nil && network.Contains(peer))
		}
		if !allowed {
			return apierror.New(403, "client_network_rejected")
		}
	}
	if s.Config.RequireTLS && r.TLS == nil && !peer.IsLoopback() {
		return apierror.New(403, "secure_transport_required")
	}
	return nil
}
func (s *Server) originOK(r *http.Request) bool {
	expected := s.Config.PublicOrigin
	if expected == "" {
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		expected = scheme + "://" + r.Host
	}
	return r.Header.Get("Origin") != "" && r.Header.Get("Origin") == expected
}
func headers(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
}
func write(w http.ResponseWriter, status int, value any) {
	headers(w)
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(value)
}
func writeError(w http.ResponseWriter, err error) {
	status := 502
	value := map[string]any{"ok": false, "error": "backend_unavailable"}
	var access *apierror.Error
	var upstream *daemon.Error
	if errors.As(err, &access) {
		status = access.Status
		value["error"] = access.Code
	} else if errors.As(err, &upstream) {
		status = upstream.Status
		value["error"] = upstream.Code
		value["daemon"] = upstream.Detail
	}
	if status == 429 {
		w.Header().Set("Retry-After", "300")
	}
	write(w, status, value)
}
func body(r *http.Request) (map[string]any, error) {
	if len(r.TransferEncoding) > 0 {
		return nil, apierror.Invalid("unsupported_encoding")
	}
	if strings.ToLower(strings.TrimSpace(strings.Split(r.Header.Get("Content-Type"), ";")[0])) != "application/json" {
		return nil, apierror.New(415, "json_required")
	}
	if r.ContentLength < 0 || r.ContentLength > 65536 {
		return nil, apierror.New(413, "body_too_large")
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 65537))
	if err != nil {
		return nil, apierror.Invalid("invalid_json")
	}
	if len(raw) > 65536 {
		return nil, apierror.New(413, "body_too_large")
	}
	if !utf8.Valid(raw) {
		return nil, apierror.Invalid("invalid_json")
	}
	if len(raw) == 0 {
		raw = []byte("{}")
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	var value any
	if decoder.Decode(&value) != nil {
		return nil, apierror.Invalid("invalid_json")
	}
	if decoder.Decode(new(any)) != io.EOF {
		return nil, apierror.Invalid("invalid_json")
	}
	m, ok := value.(map[string]any)
	if !ok || m == nil {
		return nil, apierror.Invalid("object_required")
	}
	return m, nil
}
func str(m map[string]any, key string) string { v, _ := m[key].(string); return v }
func stringList(m map[string]any, key string) ([]string, error) {
	v, ok := m[key]
	if !ok {
		return []string{}, nil
	}
	rows, ok := v.([]any)
	if !ok {
		return nil, apierror.Invalid("invalid_waker_scope")
	}
	out := make([]string, len(rows))
	for i, row := range rows {
		var ok bool
		out[i], ok = row.(string)
		if !ok {
			return nil, apierror.Invalid("invalid_waker_scope")
		}
	}
	return out, nil
}
func integer(m map[string]any, key string) (int64, error) {
	v, ok := m[key].(json.Number)
	if !ok {
		return 0, apierror.Invalid("invalid_request")
	}
	n, err := v.Int64()
	if err != nil {
		return 0, apierror.Invalid("invalid_request")
	}
	return n, nil
}
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !strings.HasPrefix(r.URL.Path, "/api/") {
		if s.Config.Assets != nil {
			headers(w)
			w.Header().Del("Content-Type")
			s.Config.Assets.ServeHTTP(w, r)
		} else {
			http.NotFound(w, r)
		}
		return
	}
	if r.Method == "GET" && r.URL.Path == "/api/health" {
		write(w, 200, map[string]any{"ok": true, "version": Version})
		return
	}
	if r.Method == "GET" && r.URL.Path == "/api/auth/mode" {
		mode := "legacy"
		enabled, e := s.Auth.AccountsMode()
		if e != nil {
			writeError(w, e)
			return
		}
		if enabled {
			mode = "accounts"
		}
		write(w, 200, map[string]string{"mode": mode})
		return
	}
	if err := s.transport(r); err != nil {
		writeError(w, err)
		return
	}
	if r.Method != "GET" && r.Method != "POST" {
		write(w, 405, map[string]any{"ok": false, "error": "method_not_allowed"})
		return
	}
	if r.Method == "POST" && (r.Header.Get("Sec-Fetch-Site") == "cross-site" || (r.Header.Get("Origin") != "" && !s.originOK(r))) {
		writeError(w, apierror.New(403, "origin_rejected"))
		return
	}
	if r.Method == "POST" && (r.URL.Path == "/api/login" || r.URL.Path == "/api/migrate") {
		s.login(w, r)
		return
	}
	identity, err := s.Auth.Authenticate(r)
	if err != nil {
		writeError(w, err)
		return
	}
	if r.Method == "GET" && r.URL.Path == "/api/whoami" {
		write(w, 200, map[string]any{"ok": true, "level": identity.Role, "csrf": identity.CSRF, "version": Version, "userId": nullable(identity.UserID), "username": nullable(identity.Username), "displayName": nullable(identity.DisplayName), "capabilities": auth.Capabilities(identity), "wakers": nonNil(identity.Wakers), "transportSecure": r.TLS != nil})
		return
	}
	values := map[string]any{}
	for key, v := range r.URL.Query() {
		if len(v) > 0 {
			values[key] = v[0]
		}
	}
	if r.Method == "POST" {
		if identity.Source == "cookie" && (!s.originOK(r) || !secureEqual(r.Header.Get("X-CSRF-Token"), identity.CSRF)) {
			writeError(w, apierror.New(403, "csrf_rejected"))
			return
		}
		values, err = body(r)
		if err != nil {
			writeError(w, err)
			return
		}
		if r.URL.Path == "/api/logout" {
			err = s.Auth.Logout(identity)
			if err != nil {
				writeError(w, err)
				return
			}
			http.SetCookie(w, &http.Cookie{Name: auth.Cookie, Value: "", Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: -1, Secure: r.TLS != nil})
			write(w, 200, map[string]bool{"ok": true})
			return
		}
	}
	if identity.Role == "caller" && !(r.Method == "POST" && r.URL.Path == "/api/gw") {
		writeError(w, apierror.New(403, "caller_route_rejected"))
		return
	}
	if identity.Role == "viewer" && (r.Method != "GET" || !contains([]string{"/api/state", "/api/usage", "/api/plugins", "/api/maintenance"}, r.URL.Path)) {
		writeError(w, apierror.New(403, "admin_required"))
		return
	}
	if identity.UserID != "" {
		if err = auth.Authorize(identity, r.Method, r.URL.Path, values); err != nil {
			writeError(w, err)
			return
		}
	}
	if r.Method == "POST" && !strings.HasPrefix(r.URL.Path, "/api/native/") && r.URL.Path != "/api/maintenance/exit" {
		lease, e := s.Admission.Acquire(contains([]string{"/api/gw", "/api/chat/send", "/api/chat/new", "/api/automation/run"}, r.URL.Path))
		if e != nil {
			writeError(w, e)
			return
		}
		defer lease.Release()
		r = r.WithContext(context.WithValue(r.Context(), leaseKey{}, lease))
	}
	if r.Method == "POST" && (contains([]string{"/api/gw", "/api/chat/new", "/api/chat/send", "/api/automation/run"}, r.URL.Path) || r.URL.Path == "/api/native/execute" && contains(s.Catalog.Policy.Spend, str(values, "operation"))) {
		ip, _, _ := net.SplitHostPort(r.RemoteAddr)
		for _, bucket := range []string{"spend-ip:" + ip, "spend-role:" + identity.Role} {
			ok, e := s.Auth.Limit(bucket, 20, 5*time.Minute)
			if e != nil {
				writeError(w, e)
				return
			}
			if !ok {
				writeError(w, apierror.New(429, "rate_limited"))
				return
			}
		}
	}
	if r.Method == "POST" {
		id := identity.UserID
		if id == "" {
			id = identity.Role
		}
		ok, e := s.Auth.Limit("writes:"+id, 120, time.Minute)
		if e != nil {
			writeError(w, e)
			return
		}
		if !ok {
			writeError(w, apierror.New(429, "rate_limited"))
			return
		}
	}
	var result any
	if r.Method == "GET" {
		result, err = s.get(r, identity, values)
	} else {
		s.invalidateState()
		defer s.invalidateState()
		defer s.Usage.Invalidate()
		result, err = s.post(r, identity, values)
	}
	if r.Method == "POST" {
		status := 200
		var e *apierror.Error
		var de *daemon.Error
		if err != nil {
			status = 502
			if errors.As(err, &e) {
				status = e.Status
			} else if errors.As(err, &de) {
				status = de.Status
			}
		}
		ip, _, _ := net.SplitHostPort(r.RemoteAddr)
		target := str(values, "id")
		if r.URL.Path == "/api/provider/config" {
			target = str(values, "name")
		}
		if target == "" {
			target = str(values, "wakerId")
		}
		resource := str(values, "wakerId")
		if r.URL.Path == "/api/waker/update" {
			resource = target
		}
		before, after := str(values, "baseRevision"), ""
		if r.URL.Path == "/api/skill/update" || r.URL.Path == "/api/skill/rollback" {
			before = str(values, "baseVersionId")
			target = resource + "/" + str(values, "skillId")
		}
		if response, ok := result.(map[string]any); ok {
			after, _ = response["revision"].(string)
			if err == nil {
				status = resultStatus(response)
			}
		}
		if r.URL.Path != "/api/native/file/append" {
			success := err == nil
			if response, ok := result.(map[string]any); ok && response["ok"] == false {
				success = false
			}
			if response, ok := result.(map[string]any); ok && (r.URL.Path == "/api/provider/config" || r.URL.Path == "/api/waker/models/update") && response["status"] != "verified" {
				success = false
			}
			s.Auth.Audit(ip, identity, r.URL.Path, target, status, success, resource, before, after)
		}
	}
	if err != nil {
		writeError(w, err)
		return
	}
	if file, ok := result.(download); ok {
		headers(w)
		w.Header().Set("Content-Type", file.mime)
		w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+url.PathEscape(file.name))
		w.WriteHeader(200)
		w.Write(file.data)
		return
	}
	status := resultStatus(result)
	if r.URL.Path == "/api/native/execute" {
		status = 202
	}
	write(w, status, result)
}
func resultStatus(value any) int {
	if result, ok := value.(map[string]any); ok {
		if result["status"] == "pending" || result["status"] == "unknown" || result["status"] == "saved_unverified" || result["status"] == "changed_after_save" || result["status"] == "readback_mismatch" {
			return 202
		}
		if result["ok"] == false {
			return 502
		}
	}
	return 200
}
func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}
func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
func contains(values []string, value string) bool {
	for _, v := range values {
		if v == value {
			return true
		}
	}
	return false
}
func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	values, err := body(r)
	if err != nil {
		writeError(w, err)
		return
	}
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)
	token := str(values, "token")
	if r.URL.Path == "/api/migrate" {
		if !s.originOK(r) {
			writeError(w, apierror.New(403, "origin_rejected"))
			return
		}
		cookie, e := r.Cookie("qwtk")
		if e == nil {
			token = cookie.Value
		}
	}
	var raw string
	var identity auth.Identity
	if _, ok := values["username"]; ok && r.URL.Path == "/api/login" {
		for _, bucket := range []struct {
			name  string
			limit int
		}{{"login-ip:" + ip, 30}, {"login-user:" + auth.Digest(strings.ToLower(str(values, "username")))[:32], 20}} {
			allowed, e := s.Auth.Limit(bucket.name, bucket.limit, 5*time.Minute)
			if e != nil {
				writeError(w, e)
				return
			}
			if !allowed {
				writeError(w, apierror.New(429, "login_rate_limited"))
				return
			}
		}
		raw, identity, err = s.Auth.LoginUser(str(values, "username"), str(values, "password"))
	} else {
		role := s.Auth.RoleFor(token)
		bucket := "login-failed:" + ip
		limit := 10
		if role != "" {
			bucket = "login-success:" + role
			limit = 30
		}
		allowed, e := s.Auth.Limit(bucket, limit, 5*time.Minute)
		if e != nil {
			writeError(w, e)
			return
		}
		if !allowed {
			writeError(w, apierror.New(429, "login_rate_limited"))
			return
		}
		raw, identity, err = s.Auth.LoginToken(token)
	}
	status := 200
	if err != nil {
		status = 401
	}
	s.Auth.Audit(ip, identity, r.URL.Path, "", status, err == nil, "", "", "")
	if err != nil {
		writeError(w, err)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: auth.Cookie, Value: raw, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: 604800, Secure: r.TLS != nil})
	http.SetCookie(w, &http.Cookie{Name: "qwtk", Value: "", Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: -1})
	write(w, 200, map[string]any{"ok": true, "level": identity.Role, "csrf": identity.CSRF})
}
func (s *Server) get(r *http.Request, id auth.Identity, b map[string]any) (any, error) {
	if strings.HasPrefix(r.URL.Path, "/api/native/") {
		return s.nativeGet(r.Context(), id, r.URL.Path, b)
	}
	if contains([]string{"/api/plugins", "/api/plugins/catalog", "/api/plugins/detail", "/api/plugins/installations", "/api/plugins/installed"}, r.URL.Path) {
		return s.pluginGet(r.Context(), r.URL.Path, b)
	}
	switch r.URL.Path {
	case "/api/usage":
		return s.Usage.Data()
	case "/api/backups":
		return s.backups()
	case "/api/runtime":
		return s.runtimeState(r.Context())
	case "/api/file-check":
		return s.fileCheckStatus(), nil
	case "/api/patch/state":
		return s.patchManager().Status(), nil
	case "/api/net/state":
		return s.networkState(r.Context())
	case "/api/egress":
		rows, _ := s.egress(r.Context())
		return rows, nil
	case "/api/channels":
		return s.channels(r.Context())
	case "/api/channels/detail":
		return s.channelDetail(r.Context(), str(b, "id"))
	case "/api/provider/config":
		return s.Providers.Editor(str(b, "name"))
	case "/api/models/native-byok":
		return s.nativeBYOK(r.Context())
	case "/api/waker/models":
		return s.wakerModels(r.Context(), str(b, "waker"))
	case "/api/maintenance":
		if id.Role != "admin" {
			state := s.Admission.Status()
			return map[string]any{"mode": state.Mode, "expires": state.Expires}, nil
		}
		return s.Admission.Status(), nil
	case "/api/system/status":
		return s.systemStatus(r.Context())
	case "/api/users":
		users, err := s.Auth.Users()
		if err != nil {
			return nil, err
		}
		enabled, err := s.Auth.AccountsMode()
		mode := "legacy"
		if enabled {
			mode = "accounts"
		}
		return map[string]any{"items": users, "mode": mode}, err
	case "/api/audit":
		return s.Auth.Recent(id)
	case "/api/callers":
		return s.Auth.Callers()
	case "/api/state":
		return s.fullState(r.Context(), id)
	case "/api/waker/detail":
		v, err := s.waker(r.Context(), str(b, "id"))
		if err != nil {
			return nil, err
		}
		if id.UserID != "" && id.Role != "admin" {
			v = pick(v, "agentId", "id", "name", "description", "configRevision", "skills")
			skills := []map[string]any{}
			for _, row := range rows(v["skills"]) {
				skills = append(skills, pick(row, "skillId", "name", "description", "enabled", "source", "mutableByAgent", "pinned", "currentVersionId"))
			}
			v["skills"] = skills
		}
		return v, nil
	case "/api/sessions/query":
		return s.consoleSessions(r.Context(), b)
	case "/api/session/artifacts":
		return s.sessionArtifacts(r.Context(), str(b, "session"))
	case "/api/session/artifact-file":
		return s.artifactFile(r.Context(), str(b, "session"), str(b, "artifact"))
	case "/api/chat/sessions":
		return s.chatSessions(r.Context(), str(b, "waker"))
	case "/api/chat/messages":
		return s.chatMessages(r.Context(), str(b, "session"))
	case "/api/memory":
		return s.memoryData(r.Context(), str(b, "waker"))
	case "/api/automations", "/api/automation/runs":
		return s.automations(r.Context(), b, r.URL.Path == "/api/automation/runs")
	case "/api/pairings":
		limit, e := queryInt(b, "limit", 50, 1, 100)
		if e != nil {
			return nil, e
		}
		offset, e := queryInt(b, "offset", 0, 0, 100000)
		if e != nil {
			return nil, e
		}
		return s.pendingPairings(r.Context(), limit, offset)
	case "/api/skills":
		return s.skills(r.Context(), str(b, "waker"))
	case "/api/skill/content":
		v, err := s.skill(r.Context(), str(b, "waker"), str(b, "skill"))
		if id.Role == "observer" && v != nil {
			v["editable"] = false
			v["readOnlyReason"] = "permission_denied"
		}
		return v, err
	case "/api/skill/versions":
		return s.skillVersions(r.Context(), str(b, "waker"), str(b, "skill"))
	case "/api/skill/diff":
		return s.skillDiff(r.Context(), str(b, "waker"), str(b, "skill"), str(b, "version"))
	}
	return nil, apierror.New(404, "not_found")
}
func (s *Server) post(r *http.Request, id auth.Identity, b map[string]any) (any, error) {
	if strings.HasPrefix(r.URL.Path, "/api/native/") {
		return s.nativePost(r.Context(), id, r.URL.Path, r.Header.Get("Idempotency-Key"), b)
	}
	switch r.URL.Path {
	case "/api/net/config":
		return s.applyNetwork(r, id, b)
	case "/api/net/firewall":
		return s.applyFirewall(r, b)
	case "/api/runtime/policy":
		return s.saveRuntimePolicy(r.URL.Path, b)
	case "/api/file-check":
		return s.startFileCheck(b)
	case "/api/patch/plan", "/api/patch/execute":
		return s.patchPost(r, id, b)
	case "/api/runtime/apply", "/api/apply":
		return s.applyRuntime(r, id, b)
	case "/api/backup":
		name, e := s.createBackup()
		return map[string]any{"ok": true, "name": name}, e
	case "/api/backup/delete":
		if str(b, "confirm") != r.URL.Path {
			return nil, apierror.New(409, "confirmation_required")
		}
		return map[string]any{"ok": true, "message": "已删除"}, s.deleteBackup(str(b, "name"))
	case "/api/telemetry":
		if str(b, "confirm") != r.URL.Path {
			return nil, apierror.New(409, "confirmation_required")
		}
		enabled, ok := b["enabled"].(bool)
		if !ok {
			return nil, apierror.Invalid("invalid_telemetry_request")
		}
		ok, e := s.runCLI(r.Context(), "config", "set", "telemetry", strconv.FormatBool(enabled))
		message := "daemon 配置写入失败"
		if ok {
			message = "遥测配置已保存"
		}
		return map[string]any{"ok": ok, "telemetry": enabled, "message": message}, e
	case "/api/plugins/install", "/api/plugins/remove", "/api/plugins/toggle":
		return s.pluginPost(r.Context(), id, r.URL.Path, b)
	case "/api/channels/action", "/api/channels/config", "/api/channels/delete":
		return s.channelPost(r.Context(), id, r.URL.Path, b)
	case "/api/chat/new", "/api/chat/send", "/api/gw":
		release, e := s.writeLock(id)
		if e != nil {
			return nil, e
		}
		defer release()
		if id.Role == "caller" {
			wid, e := safeID(str(b, "wakerId"))
			if e != nil {
				return nil, e
			}
			if _, e = textValue(b["message"], 16000, false); e != nil {
				return nil, e
			}
			allowed, e := s.Auth.Limit("spend-caller:"+id.CallerID, 20, 5*time.Minute)
			if e != nil {
				return nil, e
			}
			if !allowed {
				return nil, apierror.New(429, "rate_limited")
			}
			if e = s.Auth.ConsumeCaller(id, wid); e != nil {
				return nil, e
			}
		}
		return s.sessionPost(r.Context(), r.URL.Path, b)
	case "/api/session/read", "/api/session/delete":
		return s.sessionPost(r.Context(), r.URL.Path, b)
	case "/api/automation/toggle", "/api/automation/run", "/api/automation/delete", "/api/pairing/approve":
		return s.automationPost(r.Context(), id, r.URL.Path, b)
	case "/api/maintenance/exit":
		return map[string]bool{"ok": true}, s.Admission.Exit()
	case "/api/maintenance/enter":
		if str(b, "confirm") != r.URL.Path {
			return nil, apierror.New(409, "confirmation_required")
		}
		reason := str(b, "reason")
		if reason == "" {
			reason = "管理员手动维护"
		}
		ttl := int64(300)
		if _, exists := b["ttl"]; exists {
			var e error
			ttl, e = integer(b, "ttl")
			if e != nil {
				return nil, apierror.Invalid("invalid_maintenance_request")
			}
		}
		if ttl < 30 || ttl > 3600 {
			return nil, apierror.Invalid("invalid_maintenance_request")
		}
		lease, _ := r.Context().Value(leaseKey{}).(*admission.Lease)
		epoch, e := s.Admission.Begin(reason, time.Duration(ttl)*time.Second, lease)
		if e != nil {
			return nil, e
		}
		defer s.Admission.Complete(epoch)
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		if e = s.Admission.Wait(ctx, epoch); e != nil {
			return nil, e
		}
		if e = s.Admission.Transition(epoch, "maintenance"); e != nil {
			return nil, e
		}
		return struct {
			OK bool `json:"ok"`
			admission.State
		}{true, s.Admission.Status()}, nil
	case "/api/deletion/preview", "/api/deletion/status", "/api/deletion/reconcile", "/api/deletion/release", "/api/waker/delete", "/api/provider/delete":
		return s.deletePost(r.Context(), id, r.URL.Path, r.Header.Get("Idempotency-Key"), b)
	case "/api/provider", "/api/provider/probe-plan", "/api/test", "/api/test/saved":
		return s.providerPost(r.Context(), id, r.URL.Path, b)
	case "/api/provider/config":
		return s.providerConfigPost(r.Context(), id, b)
	case "/api/waker/models/update":
		return s.updateWakerModel(r.Context(), id, b)
	case "/api/preference":
		release, e := s.writeLock(id)
		if e != nil {
			return nil, e
		}
		defer release()
		return s.preference(r.Context(), str(b, "wakerId"), str(b, "model"))
	case "/api/waker":
		release, e := s.writeLock(id)
		if e != nil {
			return nil, e
		}
		defer release()
		return s.createWaker(r.Context(), b)
	case "/api/users/create":
		w, e := stringList(b, "wakers")
		if e != nil {
			return nil, e
		}
		u, e := s.Auth.CreateUser(str(b, "username"), str(b, "displayName"), str(b, "role"), w, str(b, "password"), false)
		return map[string]any{"ok": true, "user": u}, e
	case "/api/callers/create":
		if str(b, "confirm") != r.URL.Path {
			return nil, apierror.New(409, "confirmation_required")
		}
		release, e := s.writeLock(id)
		if e != nil {
			return nil, e
		}
		defer release()
		scopes, e := stringList(b, "wakers")
		if e != nil {
			return nil, e
		}
		for _, wid := range scopes {
			if _, e = s.waker(r.Context(), wid); e != nil {
				return nil, apierror.Invalid("invalid_waker_scope")
			}
		}
		quota, e := integer(b, "quota")
		if e != nil {
			return nil, apierror.Invalid("invalid_caller_quota")
		}
		days, e := integer(b, "days")
		if e != nil {
			return nil, apierror.Invalid("invalid_caller_quota")
		}
		value, e := s.Auth.CreateCaller(str(b, "name"), scopes, int(quota), int(days))
		if e != nil {
			return nil, e
		}
		return map[string]any{"ok": true, "id": value["id"], "token": value["token"]}, nil
	case "/api/callers/revoke":
		if str(b, "confirm") != r.URL.Path {
			return nil, apierror.New(409, "confirmation_required")
		}
		cid, e := safeID(str(b, "id"))
		if e != nil {
			return nil, e
		}
		return map[string]any{"ok": true}, s.Auth.RevokeCaller(cid)
	case "/api/users/update":
		enabled, ok := b["enabled"].(bool)
		revision, e := integer(b, "revision")
		if !ok || e != nil {
			return nil, apierror.Invalid("invalid_user_update")
		}
		w, e := stringList(b, "wakers")
		if e != nil {
			return nil, e
		}
		u, e := s.Auth.UpdateUser(str(b, "id"), str(b, "displayName"), str(b, "role"), w, enabled, revision)
		return map[string]any{"ok": true, "user": u}, e
	case "/api/users/password":
		revision, e := integer(b, "revision")
		if e != nil {
			return nil, apierror.Invalid("invalid_user_update")
		}
		return map[string]bool{"ok": true}, s.Auth.ResetPassword(str(b, "id"), str(b, "password"), &revision)
	case "/api/account/password":
		return map[string]bool{"ok": true}, s.Auth.ChangePassword(id, str(b, "currentPassword"), str(b, "password"))
	case "/api/waker/update", "/api/skill/update", "/api/skill/rollback":
		return s.writeResource(r.Context(), id, r.URL.Path, b)
	}
	return nil, apierror.New(404, "not_found")
}
func env(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}
func DefaultConfig() Config {
	home, _ := os.UserHomeDir()
	root := env("QW_ROOT", filepath.Join(home, "qoderwake-panel"))
	daemonHome := env("QW_HOME", filepath.Join(home, ".qoderwake"))
	gwPort := 19840
	if value := os.Getenv("QW_GW_PORT"); value != "" {
		var err error
		gwPort, err = strconv.Atoi(value)
		if err != nil {
			gwPort = -1
		}
	}
	return Config{Root: root, Home: daemonHome, DaemonURL: os.Getenv("QW_DAEMON_URL"), CLI: os.Getenv("QW_DAEMON_BIN"), TokenPath: os.Getenv("QW_DAEMON_FRONTEND_TOKEN_FILE"), CredentialService: env("QW_DAEMON_CREDENTIAL_SERVICE", "qoderwake.auth.v1"), PublicOrigin: strings.TrimRight(os.Getenv("QW_PUBLIC_ORIGIN"), "/"), AllowedClients: os.Getenv("QW_ALLOWED_CLIENTS"), RequireTLS: os.Getenv("QW_REQUIRE_TLS") == "1", PluginWrites: os.Getenv("QW_EXPERIMENTAL_PLUGIN_WRITES") == "1", Whitelist: os.Getenv("QW_WHITELIST"), GatewayPort: gwPort, GatewayConfig: os.Getenv("QW_GW_CONFIG"), GatewayUpstream: os.Getenv("QW_GW_UPSTREAM"), GatewayUser: os.Getenv("QW_GW_USER")}
}

type leaseKey struct{}
