package managed

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"qoderwake-panel/internal/platform"
)

type Config struct {
	Root, Name, Launch, Home, Mode, Endpoint, HealthURL, Log, ExpectedVersion, CAFile, ServerName string
	Port                                                                                          int
	Environment                                                                                   map[string]string
}
type Controller struct {
	Config              Config
	statePath, lockPath string
	legacyLaunch        bool
}

func New(cfg Config) (*Controller, error) {
	if cfg.Name == "" {
		cfg.Name = "daemon"
	}
	if !safeName.MatchString(cfg.Name) || cfg.Port < 1 || cfg.Port > 65535 || (cfg.Mode != "direct" && cfg.Mode != "gateway") {
		return nil, ErrState
	}
	var err error
	cfg.Root, err = filepath.Abs(cfg.Root)
	if err != nil {
		return nil, err
	}
	cfg.Home, err = filepath.Abs(cfg.Home)
	if err != nil {
		return nil, err
	}
	if value, e := filepath.EvalSymlinks(cfg.Root); e == nil {
		cfg.Root = value
	}
	if value, e := filepath.EvalSymlinks(cfg.Home); e == nil {
		cfg.Home = value
	}
	file, err := platform.FileIdentity(cfg.Launch)
	if err != nil {
		return nil, err
	}
	cfg.Launch = file.Path
	health, err := url.Parse(cfg.HealthURL)
	if err != nil || !(health.Scheme == "http" || health.Scheme == "https") || health.User != nil || health.RawQuery != "" || health.Fragment != "" || health.Path != "/api/health" || !(health.Hostname() == "127.0.0.1" || health.Hostname() == "localhost" || health.Hostname() == "::1") {
		return nil, errors.New("invalid_process_health_url")
	}
	port := 80
	if health.Scheme == "https" {
		port = 443
	}
	if health.Port() != "" {
		port, err = strconv.Atoi(health.Port())
		if err != nil {
			return nil, ErrState
		}
	}
	if port != cfg.Port || (health.Scheme != "https" && (cfg.CAFile != "" || cfg.ServerName != "")) {
		return nil, ErrState
	}
	if cfg.Mode == "gateway" {
		u, e := url.Parse(cfg.Endpoint)
		if e != nil || u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || !(u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost" || u.Hostname() == "::1") {
			return nil, errors.New("invalid_gateway_endpoint")
		}
	} else if cfg.Endpoint != "" {
		return nil, ErrState
	}
	if cfg.Log == "" {
		cfg.Log = filepath.Join(cfg.Root, "logs", cfg.Name+".log")
	}
	directory := filepath.Join(cfg.Root, "process-state")
	return &Controller{Config: cfg, statePath: filepath.Join(directory, cfg.Name+".json"), lockPath: filepath.Join(directory, cfg.Name+".lock")}, nil
}
func (c *Controller) locked() (func(), error) {
	directory := filepath.Dir(c.lockPath)
	if info, e := os.Lstat(directory); e == nil && !info.IsDir() {
		return nil, errors.New("symlink_rejected")
	}
	if e := os.MkdirAll(directory, 0700); e != nil {
		return nil, e
	}
	if e := os.Chmod(directory, 0700); e != nil {
		return nil, e
	}
	file, e := os.OpenFile(c.lockPath, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if e != nil {
		return nil, e
	}
	if e = syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
		file.Close()
		return nil, errors.New("managed_process_busy")
	}
	return func() { file.Close() }, nil
}
func (c *Controller) Read() (*State, error) {
	state, err := Read(c.statePath)
	if err != nil || state == nil {
		return state, err
	}
	endpoint := ""
	if state.Endpoint != nil {
		endpoint = *state.Endpoint
	}
	if state.Name != c.Config.Name || state.Profile != "daemon" || state.Home != c.Config.Home || state.Port != c.Config.Port || state.Mode != c.Config.Mode || endpoint != c.Config.Endpoint || state.Launch.Path != c.Config.Launch || (!reflect.DeepEqual(state.Cmdline, []string{c.Config.Launch, "start", "--foreground"}) && !reflect.DeepEqual(state.Cmdline, daemonCommand(c.Config.Launch, c.Config.Port))) {
		return nil, errors.New("managed_process_instance_mismatch")
	}
	return state, nil
}
func daemonCommand(binary string, port int) []string {
	return []string{binary, "start", "--foreground", "--host", "127.0.0.1", "--port", strconv.Itoa(port), "--no-keepalive"}
}
func (c *Controller) command() []string {
	if c.legacyLaunch {
		return []string{c.Config.Launch, "start", "--foreground"}
	}
	return daemonCommand(c.Config.Launch, c.Config.Port)
}
func (c *Controller) Environment() (map[string]string, error) {
	result := map[string]string{}
	for key, value := range c.Config.Environment {
		if DaemonKeys[key] || ProxyKeys[key] {
			result[key] = value
		}
	}
	result["QODERWAKE_HOME"] = c.Config.Home
	for key := range GatewayKeys {
		if c.Config.Mode == "gateway" {
			result[key] = c.Config.Endpoint
		} else {
			delete(result, key)
		}
	}
	for _, key := range []string{"QODERWAKE_SESSION_PROJECTION_UPLINK", "QODERWAKE_REMOTE_EXECUTION_UPLINK"} {
		if value, ok := result[key]; ok {
			if _, err := Switch(value, true); err != nil {
				return nil, err
			}
		}
	}
	return result, nil
}
func (c *Controller) childEnvironment(values map[string]string) []string {
	home, _ := os.UserHomeDir()
	env := []string{"HOME=" + home, "PATH=/opt/homebrew/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C.UTF-8"}
	for key, value := range values {
		env = append(env, key+"="+value)
	}
	return env
}
func (c *Controller) Health(ctx context.Context) (string, error) {
	config := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: c.Config.ServerName}
	if c.Config.CAFile != "" {
		roots, err := x509.SystemCertPool()
		if err != nil {
			roots = x509.NewCertPool()
		}
		raw, err := os.ReadFile(c.Config.CAFile)
		if err != nil || !roots.AppendCertsFromPEM(raw) {
			return "", errors.New("invalid_process_health_tls")
		}
		config.RootCAs = roots
	}
	transport := &http.Transport{Proxy: nil, TLSClientConfig: config, DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Timeout: 2 * time.Second, Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, err := http.NewRequestWithContext(ctx, "GET", c.Config.HealthURL, nil)
	if err != nil {
		return "", err
	}
	response, err := client.Do(req)
	if err != nil {
		return "", errors.New("managed_process_health_unavailable")
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 65537))
	if err != nil || len(raw) > 65536 || response.StatusCode != 200 || !utf8.Valid(raw) {
		return "", errors.New("managed_process_health_invalid")
	}
	var value map[string]any
	if json.Unmarshal(raw, &value) != nil {
		return "", errors.New("managed_process_health_invalid")
	}
	if data, ok := value["data"].(map[string]any); ok {
		value = data
	}
	version, ok := value["version"].(string)
	if !ok || len([]rune(version)) < 1 || len([]rune(version)) > 128 {
		return "", errors.New("managed_process_health_invalid")
	}
	return version, nil
}
func (c *Controller) Status(ctx context.Context) (map[string]any, error) {
	state, err := c.Read()
	if err != nil {
		return nil, err
	}
	if state == nil {
		return map[string]any{"ok": true, "name": c.Config.Name, "managed": false, "running": false}, nil
	}
	identity := IdentityStatus(ctx, state)
	port := "unknown"
	var version any
	if identity == "match" {
		port = platform.PortStatus(ctx, state.PID, state.Port)
	}
	if port == "owned" {
		if value, e := c.Health(ctx); e == nil {
			version = value
		}
	}
	healthy := identity == "match" && port == "owned" && state.Status == "running" && state.HealthVersion != nil && version == *state.HealthVersion && c.supervision(ctx, state) == nil
	return map[string]any{"ok": true, "name": state.Name, "managed": true, "running": identity == "match", "healthy": healthy, "identity": identity, "port": port, "pid": state.PID, "mode": state.Mode, "version": version}, nil
}
func (c *Controller) capture(ctx context.Context, pid int, supervisor *Supervisor, environment map[string]string, launch platform.File) (*State, error) {
	observed, err := platform.Observe(ctx, pid)
	if err != nil {
		return nil, err
	}
	if observed.UID != uint32(os.Geteuid()) || observed.Environment["QODERWAKE_HOME"] != c.Config.Home {
		return nil, errors.New("process_home_or_uid_mismatch")
	}
	state := &State{SchemaVersion: 2, Name: c.Config.Name, Profile: "daemon", Status: "starting", PID: pid, StartTicks: observed.StartTicks, BootID: observed.BootID, UID: observed.UID, Executable: observed.Executable, Launch: File{launch, strings.Repeat("0", 64)}, Cmdline: observed.Cmdline, Home: c.Config.Home, Port: c.Config.Port, Mode: c.Config.Mode, Environment: environment, Platform: runtime.GOOS, Supervisor: supervisor}
	if c.Config.Endpoint != "" {
		endpoint := c.Config.Endpoint
		state.Endpoint = &endpoint
	}
	if err = state.Validate(); err != nil {
		return nil, err
	}
	if IdentityStatus(ctx, state) != "match" {
		return nil, errors.New("managed_process_identity_unknown")
	}
	return state, nil
}
func (c *Controller) removeState() error {
	if err := os.Remove(c.statePath); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
func (c *Controller) Stop(ctx context.Context) (map[string]any, error) {
	release, err := c.locked()
	if err != nil {
		return nil, err
	}
	defer release()
	return c.stop(ctx)
}
func (c *Controller) stop(ctx context.Context) (map[string]any, error) {
	state, err := c.Read()
	if err != nil {
		return nil, err
	}
	if state == nil {
		listeners, e := platform.Listeners(ctx, c.Config.Port)
		if e != nil {
			return nil, e
		}
		if len(listeners) > 0 {
			return nil, errors.New("unmanaged_process_port_occupied")
		}
		return map[string]any{"ok": true, "stopped": true, "name": c.Config.Name}, nil
	}
	identity := IdentityStatus(ctx, state)
	if identity == "exited" {
		listeners, e := platform.Listeners(ctx, c.Config.Port)
		if e != nil {
			return nil, e
		}
		if len(listeners) > 0 {
			return nil, errors.New("managed_process_stop_port_occupied")
		}
		if err = c.cleanupSupervisor(ctx, state); err != nil {
			return nil, err
		}
		return map[string]any{"ok": true, "stopped": true, "name": c.Config.Name}, c.removeState()
	}
	if identity != "match" {
		return nil, errors.New("managed_process_identity_unknown")
	}
	check := func() error {
		if IdentityStatus(ctx, state) != "match" {
			return errors.New("managed_process_identity_unknown")
		}
		port := platform.PortStatus(ctx, state.PID, state.Port)
		if port != "owned" && port != "unbound" {
			return errors.New("managed_process_port_visibility_required")
		}
		return nil
	}
	if err = check(); err != nil {
		return nil, err
	}
	if err = c.terminate(ctx, state, check); err != nil {
		return nil, err
	}
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		if IdentityStatus(ctx, state) == "exited" {
			listeners, e := platform.Listeners(ctx, c.Config.Port)
			if e != nil {
				return nil, e
			}
			if len(listeners) > 0 {
				return nil, errors.New("managed_process_stop_port_occupied")
			}
			return map[string]any{"ok": true, "stopped": true, "name": c.Config.Name}, c.removeState()
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-deadline.C:
			return nil, errors.New("managed_process_stop_timeout")
		case <-ticker.C:
		}
	}
}
func (c *Controller) Start(ctx context.Context) (map[string]any, error) {
	release, err := c.locked()
	if err != nil {
		return nil, err
	}
	defer release()
	environment, err := c.Environment()
	if err != nil {
		return nil, err
	}
	previous, err := c.Read()
	if err != nil {
		return nil, err
	}
	c.legacyLaunch = previous != nil && reflect.DeepEqual(previous.Cmdline, []string{c.Config.Launch, "start", "--foreground"})
	if previous != nil {
		for _, key := range []string{"QODERWAKE_SESSION_PROJECTION_UPLINK", "QODERWAKE_REMOTE_EXECUTION_UPLINK"} {
			old, present := previous.Environment[key]
			before, e := Switch(old, present)
			if e != nil {
				return nil, e
			}
			value, present := environment[key]
			after, e := Switch(value, present)
			if e != nil {
				return nil, e
			}
			if before == false && after != false {
				return nil, errors.New("managed_uplink_disable_not_preserved")
			}
		}
		if _, err = c.stop(ctx); err != nil {
			return nil, err
		}
	} else {
		listeners, e := platform.Listeners(ctx, c.Config.Port)
		if e != nil {
			return nil, e
		}
		if len(listeners) > 0 {
			return nil, errors.New("unmanaged_process_port_occupied")
		}
	}
	launch, err := platform.FileIdentity(c.Config.Launch)
	if err != nil {
		return nil, err
	}
	pid, supervisor, rollback, err := c.spawn(ctx, environment)
	if err != nil {
		return nil, err
	}
	accepted := false
	defer func() {
		if !accepted && rollback != nil {
			_ = rollback()
		}
	}()
	deadline := time.NewTimer(60 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	var state *State
	for {
		state, err = c.capture(ctx, pid, supervisor, environment, launch)
		if err == nil {
			if err = WriteState(c.statePath, state); err != nil {
				return nil, err
			}
			if platform.PortStatus(ctx, pid, c.Config.Port) == "owned" {
				if version, e := c.Health(ctx); e == nil && (c.Config.ExpectedVersion == "" || version == c.Config.ExpectedVersion) {
					state.Status = "running"
					state.HealthVersion = &version
					if e = WriteState(c.statePath, state); e != nil {
						return nil, e
					}
					accepted = true
					return map[string]any{"ok": true, "name": state.Name, "pid": pid, "port": state.Port, "mode": state.Mode, "version": version}, nil
				}
			}
		}
		if errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
			c.removeState()
			return nil, errors.New("managed_process_start_failed")
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-deadline.C:
			if state != nil && IdentityStatus(ctx, state) == "match" {
				_, _ = c.stop(context.WithoutCancel(ctx))
			}
			return nil, errors.New("managed_process_readiness_timeout")
		case <-ticker.C:
		}
	}
}
