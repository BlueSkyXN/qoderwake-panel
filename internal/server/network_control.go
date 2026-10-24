package server

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"qoderwake-panel/internal/admission"
	"qoderwake-panel/internal/apierror"
	"qoderwake-panel/internal/auth"
	"qoderwake-panel/internal/gateway"
)

func (s *Server) gatewayControl() (*gateway.Control, error) {
	binary := s.Config.GatewayBinary
	if binary == "" {
		var err error
		binary, err = os.Executable()
		if err != nil {
			return nil, err
		}
	}
	port := s.Config.GatewayPort
	if port == 0 {
		port = 19840
	}
	return gateway.NewControl(s.Config.Root, binary, s.Config.GatewayConfig, s.Config.GatewayUpstream, s.Config.GatewayUser, port)
}
func (s *Server) networkPolicy() (*gateway.Policy, error) {
	path := s.Config.GatewayConfig
	if path == "" {
		path = filepath.Join(canonical(s.Config.Root), "config", "uplink-gw.json")
	}
	p, err := gateway.ReadPolicy(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, apierror.Invalid("invalid_gateway_config")
	}
	return &p, nil
}
func (s *Server) networkState(ctx context.Context) (any, error) {
	control, controlError := s.gatewayControl()
	active := map[string]any{"managed": false, "healthy": false}
	port := s.Config.GatewayPort
	if port == 0 {
		port = 19840
	}
	var firewall map[string]any
	if controlError == nil {
		active = control.Status(ctx)
		firewall = gateway.NewFirewall(control.Native.Root, control.Native.UID).Status(ctx)
	} else {
		active["error"] = managedCode(controlError)
		firewall = map[string]any{"supported": runtime.GOOS == "linux", "available": false, "active_rules": -1, "error": managedCode(controlError)}
	}
	policy, err := s.networkPolicy()
	if err != nil {
		return nil, err
	}
	var cfg any = map[string]any{"mode": "unconfigured", "token_policy": "balanced", "sink_post_prefixes": []string{}, "token_allow_prefixes": []string{}, "rules": []any{}, "audit_paths": []string{}}
	revision := "missing"
	if policy != nil {
		cfg = policy
		revision = policy.Hash()
	}
	active["matchesSaved"] = active["healthy"] == true && policy != nil && active["configHash"] == revision
	root := canonical(s.Config.Root)
	log := filepath.Join(root, "logs", "gateway", "uplink-gw.jsonl")
	paths := []string{log}
	if _, err := os.Lstat(log); os.IsNotExist(err) {
		paths = []string{filepath.Join(root, "logs", "uplink-gw.jsonl")}
	} else {
		for i := 1; i <= 3; i++ {
			paths = append(paths, log+"."+strconv.Itoa(i))
		}
	}
	window, err := gateway.Tail(paths, 5000, 1024*1024)
	if err != nil {
		window.Available = false
	}
	type aggregate struct {
		Count  int
		Bytes  int64
		Status int
	}
	up := map[string]*aggregate{}
	poll := map[string]int{}
	total := 0
	var upload int64
	for _, line := range window.Lines {
		var row struct {
			Path   string `json:"path"`
			Method string `json:"method"`
			Size   int64  `json:"q"`
			Status *int   `json:"status"`
		}
		if json.Unmarshal([]byte(line), &row) != nil || row.Path == "" || row.Method == "" || row.Size < 0 || row.Status == nil {
			continue
		}
		total++
		parts := strings.Split(row.Path, "/")
		prefix := strings.Join(parts[:min(5, len(parts))], "/")
		if row.Method == "GET" || row.Method == "HEAD" {
			poll[prefix]++
			continue
		}
		item := up[prefix]
		if item == nil {
			item = &aggregate{}
			up[prefix] = item
		}
		item.Count++
		item.Bytes += row.Size
		item.Status = *row.Status
		upload += row.Size
	}
	uplink := []map[string]any{}
	for path, item := range up {
		uplink = append(uplink, map[string]any{"path": path, "n": item.Count, "bytes": item.Bytes, "last_status": item.Status})
	}
	sort.Slice(uplink, func(i, j int) bool {
		if uplink[i]["bytes"] == uplink[j]["bytes"] {
			return str(uplink[i], "path") < str(uplink[j], "path")
		}
		return uplink[i]["bytes"].(int64) > uplink[j]["bytes"].(int64)
	})
	polls := [][2]any{}
	for path, n := range poll {
		polls = append(polls, [2]any{path, n})
	}
	sort.Slice(polls, func(i, j int) bool { return polls[i][1].(int) > polls[j][1].(int) })
	polls = polls[:min(8, len(polls))]
	egress, known := s.egress(ctx)
	destinations := map[string]int{}
	for _, row := range egress {
		host, _, err := net.SplitHostPort(str(row, "dest"))
		if err == nil {
			destinations[host]++
		}
	}
	residual := [][2]any{}
	for ip, n := range destinations {
		residual = append(residual, [2]any{ip, n})
	}
	sort.Slice(residual, func(i, j int) bool { return residual[i][1].(int) > residual[j][1].(int) })
	return map[string]any{"gw_alive": active["healthy"] == true, "gw_port": port, "cfg": cfg, "configRevision": revision, "active": active, "fw": firewall, "total": total, "up_bytes": upload, "logWindow": window, "uplink": uplink, "poll": polls, "residual": residual, "egressAvailable": known, "canConfigure": controlError == nil}, nil
}
func (s *Server) validateNetwork(b map[string]any) (gateway.Policy, string, error) {
	saved, err := s.networkPolicy()
	if err != nil {
		return gateway.Policy{}, "", err
	}
	p := gateway.DefaultPolicy()
	revision := "missing"
	if saved != nil {
		p = *saved
		revision = p.Hash()
	}
	if base := str(b, "baseRevision"); base != "" && base != revision {
		return p, "", apierror.New(409, "version_conflict")
	}
	if value, ok := b["mode"]; ok {
		mode, ok := value.(string)
		if !ok {
			return p, "", apierror.Invalid("invalid_gateway_mode")
		}
		if mode == "strict" {
			return p, "", apierror.Invalid("strict_not_validated")
		}
		if mode != "observe" && mode != "enforce" {
			return p, "", apierror.Invalid("invalid_gateway_mode")
		}
		p.Mode = mode
	}
	if value, ok := b["token_policy"]; ok && value != "balanced" {
		return p, "", apierror.Invalid("strict_not_validated")
	}
	if value, ok := b["sink_post_prefixes"]; ok {
		list, ok := value.([]any)
		if !ok || len(list) > 200 {
			return p, "", apierror.Invalid("invalid_gateway_paths")
		}
		allowed := map[string]bool{"/algo/api/v1/tracking": true}
		for _, path := range p.Sink {
			allowed[path] = true
		}
		p.Sink = []string{}
		for _, item := range list {
			path, ok := item.(string)
			if !ok || !allowed[path] {
				return p, "", apierror.Invalid("unvalidated_uplink_rule")
			}
			p.Sink = append(p.Sink, path)
		}
	}
	if err = p.Validate(); err != nil {
		return p, "", apierror.Invalid(managedCode(err))
	}
	return p, revision, nil
}
func (s *Server) applyNetwork(r *http.Request, id auth.Identity, b map[string]any) (any, error) {
	if str(b, "confirm") != "/api/net/config" {
		return nil, apierror.New(409, "confirmation_required")
	}
	p, revision, err := s.validateNetwork(b)
	if err != nil {
		return nil, err
	}
	control, err := s.gatewayControl()
	if err != nil {
		return nil, apierror.New(409, managedCode(err))
	}
	finish, err := s.beginOperation()
	if err != nil {
		return nil, err
	}
	defer finish()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 150*time.Second)
	defer cancel()
	if _, err = control.Preflight(ctx, p); err != nil {
		return nil, apierror.New(409, managedCode(err))
	}
	lease, _ := r.Context().Value(leaseKey{}).(*admission.Lease)
	epoch, err := s.Admission.Begin("切换控制面网关策略", 3*time.Minute, lease)
	if err != nil {
		return nil, err
	}
	defer s.Admission.Complete(epoch)
	drain, stop := context.WithTimeout(ctx, 30*time.Second)
	err = s.Admission.Wait(drain, epoch)
	stop()
	if err != nil {
		return nil, err
	}
	idle, stop := context.WithTimeout(ctx, 30*time.Second)
	err = s.waitDaemonIdle(idle)
	stop()
	if err != nil {
		return nil, err
	}
	id, err = s.Auth.Refresh(id)
	if err != nil {
		return nil, err
	}
	if id.Role != "admin" {
		return nil, apierror.New(403, "admin_required")
	}
	saved, err := s.networkPolicy()
	if err != nil {
		return nil, err
	}
	current := "missing"
	if saved != nil {
		current = saved.Hash()
	}
	if current != revision {
		return nil, apierror.New(409, "version_conflict")
	}
	if err = s.Admission.Transition(epoch, "restarting"); err != nil {
		return nil, err
	}
	result, err := control.Apply(ctx, p)
	if err != nil {
		return nil, apierror.New(502, managedCode(err))
	}
	s.invalidateState()
	result["message"] = "网关策略已应用并通过健康检查"
	return result, nil
}
func (s *Server) applyFirewall(r *http.Request, b map[string]any) (any, error) {
	if str(b, "confirm") != "/api/net/firewall" {
		return nil, apierror.New(409, "confirmation_required")
	}
	c, err := s.gatewayControl()
	if err != nil {
		return nil, apierror.New(409, managedCode(err))
	}
	finish, err := s.beginOperation()
	if err != nil {
		return nil, err
	}
	defer finish()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 30*time.Second)
	defer cancel()
	result, err := gateway.NewFirewall(c.Native.Root, c.Native.UID).Apply(ctx, str(b, "action"), str(b, "baseRevision"))
	if err != nil {
		code := managedCode(err)
		status := 502
		if errors.Is(err, context.DeadlineExceeded) {
			code = "firewall_operation_timeout"
		}
		return nil, apierror.New(status, code)
	}
	return result, nil
}
