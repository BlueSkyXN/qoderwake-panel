package server

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"qoderwake-panel/internal/admission"
	"qoderwake-panel/internal/apierror"
	"qoderwake-panel/internal/auth"
	"qoderwake-panel/internal/managed"
	"qoderwake-panel/internal/platform"
)

var regexpCode = regexp.MustCompile(`^[a-z][a-z0-9_]+$`)

func canonical(path string) string {
	if value, e := filepath.EvalSymlinks(path); e == nil {
		return value
	}
	return path
}
func (s *Server) daemonController(ctx context.Context) (*managed.Controller, *managed.State, error) {
	endpoint, err := url.Parse(s.Config.DaemonURL)
	if err != nil {
		return nil, nil, err
	}
	if endpoint.Hostname() != "127.0.0.1" && endpoint.Hostname() != "localhost" && endpoint.Hostname() != "::1" {
		return nil, nil, errors.New("daemon_identity_unverified")
	}
	port := 80
	if endpoint.Scheme == "https" {
		port = 443
	}
	if endpoint.Port() != "" {
		port, err = strconv.Atoi(endpoint.Port())
		if err != nil {
			return nil, nil, err
		}
	}
	var selected *managed.State
	for _, name := range []string{"daemon", "daemon-cn"} {
		state, e := managed.Read(filepath.Join(s.Config.Root, "process-state", name+".json"))
		if e != nil {
			return nil, nil, e
		}
		if state == nil {
			continue
		}
		if state.Home == canonical(s.Config.Home) && state.Launch.Path == canonical(s.Config.CLI) && state.Port == port {
			if selected != nil {
				return nil, nil, errors.New("ambiguous_managed_instance")
			}
			selected = state
		}
	}
	if selected == nil {
		return nil, nil, nil
	}
	cfg := managed.Config{Root: s.Config.Root, Name: selected.Name, Launch: s.Config.CLI, Home: s.Config.Home, Port: port, Mode: selected.Mode, HealthURL: strings.TrimRight(s.Config.DaemonURL, "/") + "/api/health", Environment: selected.Environment}
	if selected.Endpoint != nil {
		cfg.Endpoint = *selected.Endpoint
	}
	controller, err := managed.New(cfg)
	if err != nil {
		return nil, nil, err
	}
	status, err := controller.Status(ctx)
	if err != nil {
		return nil, nil, err
	}
	if status["healthy"] != true {
		return nil, nil, errors.New("daemon_identity_unverified")
	}
	return controller, selected, nil
}
func runtimeBase(state *managed.State) map[string]string {
	result := map[string]string{}
	for _, entry := range os.Environ() {
		key, value, ok := strings.Cut(entry, "=")
		if ok && (managed.DaemonKeys[key] || managed.ProxyKeys[key]) {
			result[key] = value
		}
	}
	if state != nil {
		for key, value := range state.Environment {
			result[key] = value
		}
	}
	return result
}
func (s *Server) runtimeState(ctx context.Context) (map[string]any, error) {
	policy, err := managed.ReadPolicy(s.Config.Root)
	if err != nil {
		return nil, apierror.Invalid("invalid_runtime_policy")
	}
	_, state, _ := s.daemonController(ctx)
	effective, err := policy.Effective(runtimeBase(state))
	if err != nil {
		return nil, apierror.Invalid("invalid_runtime_switch")
	}
	observed := []map[string]any{}
	uplinks := map[string]any{}
	if state != nil {
		process, e := platform.Observe(ctx, state.PID)
		if e == nil {
			row := map[string]any{}
			valid := true
			for key, variable := range managed.Switches {
				value, present := process.Environment[variable]
				setting, e := managed.Switch(value, present)
				if e != nil {
					valid = false
				}
				if key == "sessionProjectionUplink" || key == "remoteExecutionUplink" {
					uplinks[key] = setting
				} else {
					row[key] = setting
				}
			}
			if valid && managed.IdentityStatus(ctx, state) == "match" {
				observed = append(observed, row)
			} else {
				uplinks = map[string]any{}
			}
		}
	}
	known := len(observed) > 0 && observed[0]["hotDeploy"] != nil && observed[0]["embeddingDisabled"] != nil
	var pending any
	if known {
		changed := observed[0]["hotDeploy"] != policy["hotDeploy"] || observed[0]["embeddingDisabled"] != policy["embeddingDisabled"]
		for _, key := range []string{"sessionProjectionUplink", "remoteExecutionUplink"} {
			if effective[key] != nil && effective[key] != uplinks[key] {
				changed = true
			}
		}
		pending = changed
	}
	sdkPending := len(observed) > 0 && (observed[0]["sdkByok"] == true) != (effective["sdkByok"] == true)
	observation := "unknown"
	if known {
		observation = "verified"
	}
	allowed := state != nil && len(observed) > 0
	var blocker any
	if !allowed {
		blocker = "daemon_identity_unverified"
	}
	return map[string]any{"desired": policy, "observed": observed, "observedUplink": uplinks, "effectiveOnPanelRestart": effective, "pendingRestart": pending, "sdkByokPending": sdkPending, "observation": observation, "restartAllowed": allowed, "restartBlocker": blocker}, nil
}
func (s *Server) saveRuntimePolicy(path string, b map[string]any) (any, error) {
	if str(b, "confirm") != path {
		return nil, apierror.New(409, "confirmation_required")
	}
	for _, key := range []string{"hotDeploy", "embeddingDisabled"} {
		if _, ok := b[key].(bool); !ok {
			return nil, apierror.Invalid("invalid_runtime_policy")
		}
	}
	policy, err := managed.ReadPolicy(s.Config.Root)
	if err != nil {
		return nil, apierror.Invalid("invalid_runtime_policy")
	}
	for key := range managed.Switches {
		if value, exists := b[key]; exists {
			policy[key] = value
		}
	}
	if err = managed.SavePolicy(s.Config.Root, policy); err != nil {
		if errors.Is(err, managed.ErrPolicy) {
			return nil, apierror.Invalid("invalid_runtime_policy")
		}
		return nil, err
	}
	return map[string]any{"ok": true, "message": "启动策略已保存，将在下次启动时应用"}, nil
}
func (s *Server) beginOperation() (func(), error) {
	s.operationMu.Lock()
	defer s.operationMu.Unlock()
	if s.closing {
		return nil, apierror.New(503, "panel_stopping")
	}
	s.operations.Add(1)
	return s.operations.Done, nil
}
func (s *Server) applyRuntime(r *http.Request, identity auth.Identity, b map[string]any) (any, error) {
	if str(b, "confirm") != r.URL.Path {
		return nil, apierror.New(409, "confirmation_required")
	}
	finish, err := s.beginOperation()
	if err != nil {
		return nil, err
	}
	defer finish()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 150*time.Second)
	defer cancel()
	_, state, err := s.daemonController(ctx)
	if err != nil || state == nil {
		return nil, apierror.New(409, "daemon_identity_unverified")
	}
	lease, _ := r.Context().Value(leaseKey{}).(*admission.Lease)
	epoch, err := s.Admission.Begin("应用 daemon 启动策略", 3*time.Minute, lease)
	if err != nil {
		return nil, err
	}
	defer s.Admission.Complete(epoch)
	drain, stopDrain := context.WithTimeout(ctx, 30*time.Second)
	err = s.Admission.Wait(drain, epoch)
	stopDrain()
	if err != nil {
		return nil, err
	}
	idle, stopIdle := context.WithTimeout(ctx, 30*time.Second)
	err = s.waitDaemonIdle(idle)
	stopIdle()
	if err != nil {
		return nil, err
	}
	if identity, err = s.Auth.Refresh(identity); err != nil {
		return nil, err
	}
	if identity.Role != "admin" {
		return nil, apierror.New(403, "admin_required")
	}
	// 排空后重新读取记录和策略，避免等待期间已经完成的写入被旧快照覆盖。
	controller, state, err := s.daemonController(ctx)
	if err != nil || state == nil {
		return nil, apierror.New(409, "daemon_identity_unverified")
	}
	policy, err := managed.ReadPolicy(s.Config.Root)
	if err != nil {
		return nil, apierror.Invalid("invalid_runtime_policy")
	}
	controller.Config.Environment, err = policy.Environment(runtimeBase(state))
	if err != nil {
		return nil, apierror.Invalid("invalid_runtime_policy")
	}
	if err = s.Admission.Transition(epoch, "restarting"); err != nil {
		return nil, err
	}
	result, err := controller.Start(ctx)
	if err != nil {
		return nil, apierror.New(502, managedCode(err))
	}
	result["message"] = "daemon 已重启并通过身份与健康检查"
	s.invalidateState()
	return result, nil
}
func managedCode(err error) string {
	code := err.Error()
	if len(code) > 120 || !regexpCode.MatchString(code) {
		return "managed_process_operation_failed"
	}
	return code
}
