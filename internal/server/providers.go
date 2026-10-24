package server

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"qoderwake-panel/internal/apierror"
	"qoderwake-panel/internal/auth"
	"qoderwake-panel/internal/daemon"
	"qoderwake-panel/internal/provider"
)

func (s *Server) createWaker(ctx context.Context, b map[string]any) (any, error) {
	if err := bodyStrings(b, "name", "model"); err != nil {
		return nil, err
	}
	name, model := strings.TrimSpace(str(b, "name")), strings.TrimSpace(str(b, "model"))
	if name == "" || len([]rune(name)) > 100 || len([]rune(model)) > 300 {
		return nil, apierror.Invalid("invalid_text")
	}
	raw, err := s.Daemon.JSON(ctx, "POST", "/api/agents", map[string]any{"name": name, "title": name})
	if err != nil {
		return nil, err
	}
	wid := str(daemon.Data(raw), "agentId")
	if _, err = safeID(wid); err != nil {
		return nil, apierror.New(502, "invalid_waker_detail")
	}
	result := map[string]any{"ok": true, "id": wid}
	if model != "" {
		value, e := s.preference(ctx, wid, model)
		applied := false
		if response, ok := value.(map[string]any); ok && e == nil {
			applied = response["ok"] == true
		}
		result["preferenceApplied"] = applied
	}
	return result, nil
}

func (s *Server) fullState(ctx context.Context, identity auth.Identity) (map[string]any, error) {
	state, err := s.cachedState(ctx)
	if err != nil {
		return nil, err
	}
	state["legacyProviderBackups"] = nil
	if identity.UserID != "" && identity.Role != "admin" {
		wakers := []map[string]any{}
		for _, waker := range state["wakers"].([]map[string]any) {
			if contains(identity.Wakers, str(waker, "id")) {
				wakers = append(wakers, pick(waker, "id", "name", "preference"))
			}
		}
		return map[string]any{"version": state["version"], "daemon": state["daemon"], "error": state["error"], "wakers": wakers, "models": []any{}, "providers": []any{}, "whoami": nil, "providerRevision": nil, "legacyProviderBackups": nil, "pluginWritesEnabled": false, "whitelist": []any{}}, nil
	}
	if identity.Role == "admin" {
		rows, revision, e := s.Providers.Summaries()
		if e != nil {
			return nil, e
		}
		state["providers"] = rows
		state["providerRevision"] = revision
		state["legacyProviderBackups"] = s.Providers.LegacyBackups()
		state["pluginWritesEnabled"] = s.Config.PluginWrites
		_, managed, err := s.daemonController(ctx)
		state["daemonManaged"] = err == nil && managed != nil
		state["whitelist"] = s.whitelist()
	} else {
		state["whoami"] = nil
	}
	var cfg map[string]any
	if raw, e := os.ReadFile(filepath.Join(s.Config.Home, "config/config.json")); e == nil {
		json.Unmarshal(raw, &cfg)
	}
	state["telemetry"] = nil
	if enabled, ok := cfg["telemetry"].(bool); ok {
		state["telemetry"] = enabled
	}
	state["guard"] = nil
	if st, e := os.Stat(filepath.Join(s.Config.Home, "runtime-generations")); e == nil && st.IsDir() {
		state["guard"] = st.Mode().Perm()&0222 == 0
	}
	return state, nil
}
func (s *Server) writeLock(identity auth.Identity) (func(), error) {
	if !s.resources.TryLock() {
		return nil, apierror.New(409, "resource_mutation_in_progress")
	}
	release := s.resources.Unlock
	if _, err := s.Auth.Refresh(identity); err != nil {
		release()
		return nil, err
	}
	blocked, err := s.Auth.HasUnresolvedDeletions()
	if err != nil {
		release()
		return nil, err
	}
	if blocked {
		release()
		return nil, apierror.New(409, "deletion_operation_unresolved")
	}
	return release, nil
}
func bodyStrings(body map[string]any, keys ...string) error {
	for _, key := range keys {
		if value, exists := body[key]; exists {
			if _, ok := value.(string); !ok {
				return apierror.Invalid("invalid_text")
			}
		}
	}
	return nil
}
func (s *Server) providerPost(ctx context.Context, identity auth.Identity, path string, b map[string]any) (any, error) {
	if err := bodyStrings(b, "name", "baseUrl", "apiKey", "model", "display", "baseRevision", "mode", "plan"); err != nil {
		return nil, err
	}
	if path == "/api/provider" {
		release, err := s.writeLock(identity)
		if err != nil {
			return nil, err
		}
		defer release()
		snapshot, err := s.Providers.Write(str(b, "name"), str(b, "baseUrl"), str(b, "apiKey"), str(b, "model"), str(b, "display"), str(b, "baseRevision"))
		return map[string]any{"ok": true, "providerRevision": snapshot.Revision}, err
	}
	mode := str(b, "mode")
	if path == "/api/test" {
		mode = "adhoc"
	} else if path == "/api/test/saved" {
		mode = "saved"
	}
	name, revision, address, key := strings.TrimSpace(str(b, "name")), str(b, "baseRevision"), str(b, "baseUrl"), strings.TrimSpace(str(b, "apiKey"))
	if mode == "saved" {
		if name == "" || len([]rune(name)) > 120 {
			return nil, apierror.Invalid("invalid_text")
		}
		if err := provider.RequireRevision(revision); err != nil {
			return nil, err
		}
		snapshot, err := s.Providers.Read()
		if err != nil {
			return nil, err
		}
		if snapshot.Revision != revision {
			return nil, apierror.New(409, "version_conflict")
		}
		value := snapshot.Data["providers"].(map[string]any)[name]
		if value == nil {
			return nil, apierror.Invalid("provider_not_found")
		}
		if editable, _ := provider.Editability(value); !editable {
			return nil, apierror.Invalid("provider_requires_official_editor")
		}
		item := value.(map[string]any)
		address, _ = item["baseUrl"].(string)
		key, _ = item["apiKey"].(string)
	} else if mode == "adhoc" {
		if key == "" || len([]rune(key)) > 2048 {
			return nil, apierror.Invalid("invalid_text")
		}
	} else {
		return nil, apierror.Invalid("invalid_provider_probe_mode")
	}
	var err error
	address, err = provider.ValidateURL(address)
	if err != nil {
		return nil, err
	}
	if path == "/api/provider/probe-plan" {
		plan, err := s.Probes.Create(identity.Principal, mode, name, revision, address, key)
		return struct {
			OK bool `json:"ok"`
			provider.Plan
		}{true, plan}, err
	}
	if str(b, "confirm") != path {
		return nil, apierror.New(409, "confirmation_required")
	}
	allowed, err := s.Auth.Limit("provider-probe:"+identity.Principal, 10, 5*time.Minute)
	if err != nil {
		return nil, err
	}
	if !allowed {
		return nil, apierror.New(429, "rate_limited")
	}
	if _, err = s.Probes.Consume(str(b, "plan"), identity.Principal, mode, name, revision, address, key); err != nil {
		return nil, err
	}
	ok, message, err := provider.Probe(ctx, address, key)
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": ok, "message": message}, nil
}
func (s *Server) runCLI(ctx context.Context, args ...string) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, s.Config.CLI, args...)
	cmd.Env = s.Jobs.Environment()
	cmd.Dir = s.Config.Home
	err := cmd.Run()
	if err != nil {
		if _, ok := err.(*exec.ExitError); ok {
			return false, nil
		}
		return false, apierror.New(502, "cli_execution_failed")
	}
	return true, nil
}
func (s *Server) preference(ctx context.Context, waker, model string) (any, error) {
	if _, err := safeID(waker); err != nil {
		return nil, err
	}
	model = strings.TrimSpace(model)
	if model == "" || len([]rune(model)) > 300 {
		return nil, apierror.Invalid("invalid_text")
	}
	ok, err := s.runCLI(ctx, "models", "preference", "set", "--waker-id", waker, "--model", model)
	message := "默认模型更新失败"
	if ok {
		message = "已更新默认模型"
	}
	return map[string]any{"ok": ok, "message": message}, err
}
func (s *Server) wakerPreference(ctx context.Context, waker string) string {
	raw, err := s.Daemon.JSON(ctx, "GET", "/api/model-preferences/"+url.PathEscape(waker), nil)
	if err != nil {
		return "(未知)"
	}
	data, _ := raw["data"].(map[string]any)
	value, exists := data["noProject"]
	if !exists {
		if _, ok := data["byProject"]; !ok {
			return "(未知)"
		}
	}
	if value == nil || value == "" {
		return "(默认 auto)"
	}
	if model, ok := value.(string); ok {
		return model
	}
	if model, ok := value.(map[string]any); ok {
		first, _ := model["modelId"].(string)
		second, _ := model["model"].(string)
		if first != "" && (second == "" || first == second) {
			return first
		}
		if second != "" && first == "" {
			return second
		}
	}
	return "(未知)"
}
