package server

import (
	"context"
	"errors"
	"net/url"
	"sort"
	"strings"

	"qoderwake-panel/internal/apierror"
	"qoderwake-panel/internal/auth"
	"qoderwake-panel/internal/daemon"
	"qoderwake-panel/internal/deletion"
	"qoderwake-panel/internal/jsoncompat"
	"qoderwake-panel/internal/provider"
)

func (s *Server) nativeBYOK(ctx context.Context) (any, error) {
	raw, err := s.Daemon.JSON(ctx, "GET", "/api/models/byok", nil)
	result := map[string]any{"management": "official_local", "status": "available", "storage": "native", "writable": false}
	if err != nil {
		result["status"] = "unavailable"
		result["code"] = modelErrorCode(err)
		var e *daemon.Error
		if errors.As(err, &e) {
			if e.Code == "MODEL_SETTINGS_LOCAL_ONLY" {
				result["status"] = "local_only"
			}
			if e.Status == 404 {
				result["status"] = "unsupported"
			}
		}
		return result, nil
	}
	// Management stays with the official local UI. Readability is not a write grant.
	result["overview"] = provider.RedactedConfig(daemon.Data(raw))
	return result, nil
}

func (s *Server) providerConfigPost(ctx context.Context, identity auth.Identity, b map[string]any) (any, error) {
	if str(b, "confirm") != "/api/provider/config" {
		return nil, apierror.New(409, "confirmation_required")
	}
	if err := bodyStrings(b, "name", "configJSON", "keyAction", "apiKey", "baseRevision"); err != nil {
		return nil, err
	}
	release, err := s.writeLock(identity)
	if err != nil {
		return nil, err
	}
	defer release()
	before, err := s.Providers.Read()
	if err != nil {
		return nil, err
	}
	if before.Revision != str(b, "baseRevision") {
		return nil, apierror.New(409, "version_conflict")
	}
	name := str(b, "name")
	draft, err := provider.DecodeConfig(str(b, "configJSON"))
	if err != nil {
		return nil, err
	}
	old, ok := before.Data["providers"].(map[string]any)[name].(map[string]any)
	if !ok {
		return nil, apierror.New(404, "provider_not_found")
	}
	if err = provider.ValidateConfig(draft, old); err != nil {
		return nil, err
	}
	removed := []any{}
	retained := provider.Models(name, draft)
	for _, model := range provider.Models(name, old) {
		if !contains(retained, model) {
			removed = append(removed, model)
		}
	}
	if len(removed) > 0 {
		source := s.deletionSource(ctx)
		impact, err := (deletion.Scanner{Source: func(kind, target string) (any, error) {
			if kind == "provider_models" {
				return removed, nil
			}
			return source(kind, target)
		}}).Scan("provider", name, before.Revision)
		if err != nil {
			return nil, err
		}
		if !impact.Deletable {
			return nil, apierror.New(409, "provider_models_in_use")
		}
		if len(impact.WarningsRequired) > 0 && b["acknowledgeHistory"] != true {
			return nil, apierror.New(409, "provider_model_history_confirmation")
		}
	}
	snapshot, err := s.Providers.WriteConfig(name, str(b, "configJSON"), str(b, "keyAction"), str(b, "apiKey"), before.Revision)
	if err != nil {
		return nil, err
	}
	s.invalidateState()
	current, err := s.Providers.Editor(name)
	if err != nil {
		return map[string]any{"ok": true, "status": "saved_unverified", "revision": snapshot.Revision}, nil
	}
	if current["baseRevision"] != snapshot.Revision {
		return map[string]any{"ok": true, "status": "changed_after_save", "current": current, "revision": snapshot.Revision}, nil
	}
	return map[string]any{"ok": true, "status": "verified", "revision": snapshot.Revision, "current": current}, nil
}

func modelErrorCode(err error) string {
	var access *apierror.Error
	var upstream *daemon.Error
	if errors.As(err, &access) {
		return access.Code
	}
	if errors.As(err, &upstream) {
		return upstream.Code
	}
	return "daemon_unavailable"
}

func preferenceEntry(value any) (map[string]any, error) {
	if value == nil || value == "" {
		return map[string]any{}, nil
	}
	if s, ok := value.(string); ok {
		return map[string]any{"modelId": s}, nil
	}
	item, ok := value.(map[string]any)
	if !ok {
		return nil, apierror.New(502, "model_preference_unavailable")
	}
	model, alternate := str(item, "modelId"), str(item, "model")
	if model != "" && alternate != "" && model != alternate {
		return nil, apierror.New(502, "model_preference_unavailable")
	}
	if model == "" {
		model = alternate
	}
	if model == "" {
		return nil, apierror.New(502, "model_preference_unavailable")
	}
	out := map[string]any{"modelId": model}
	for _, key := range []string{"reasoningEffort", "contextWindow"} {
		if value, exists := item[key]; exists {
			out[key] = value
		}
	}
	return out, nil
}

func (s *Server) modelPreferences(ctx context.Context, wid string) (map[string]any, error) {
	if _, err := safeID(wid); err != nil {
		return nil, err
	}
	raw, err := s.Daemon.JSON(ctx, "GET", "/api/model-preferences/"+url.PathEscape(wid), nil)
	if err != nil {
		return nil, err
	}
	data := daemon.Data(raw)
	_, hasDefault := data["noProject"]
	_, hasProjects := data["byProject"]
	if !hasDefault && !hasProjects {
		return nil, apierror.New(502, "model_preference_unavailable")
	}
	entry, err := preferenceEntry(data["noProject"])
	if err != nil {
		return nil, err
	}
	projects := map[string]any{}
	if hasProjects {
		values, ok := data["byProject"].(map[string]any)
		if !ok {
			return nil, apierror.New(502, "model_preference_unavailable")
		}
		for key, value := range values {
			entry, err := preferenceEntry(value)
			if err != nil {
				return nil, err
			}
			projects[key] = entry
		}
	}
	return map[string]any{"noProject": entry, "byProject": projects, "revision": provider.Revision(jsoncompat.Marshal(data))}, nil
}

func (s *Server) wakerModels(ctx context.Context, wid string) (any, error) {
	prefs, err := s.modelPreferences(ctx, wid)
	if err != nil {
		return nil, err
	}
	result := map[string]any{"wakerId": wid, "preferences": prefs, "sessions": []any{}}
	raw, err := s.Daemon.JSON(ctx, "GET", "/api/sessions", nil)
	if err != nil {
		result["sessionsError"] = modelErrorCode(err)
		return result, nil
	}
	items, ok := sourceData(raw).([]any)
	if !ok {
		result["sessionsError"] = "session_models_unavailable"
		return result, nil
	}
	sessions := []map[string]any{}
	for _, item := range items {
		row, ok := item.(map[string]any)
		if !ok {
			result["sessionsError"] = "session_models_unavailable"
			return result, nil
		}
		if str(row, "agentId") != wid {
			continue
		}
		value := pick(row, "sessionId", "status", "currentModel", "modelConfigRevision", "runtimeRestartRequired", "startedAt", "lastActiveAt")
		if cfg, ok := row["currentModelRuntimeConfig"].(map[string]any); ok {
			value["runtimeConfig"] = pick(cfg, "model", "reasoningEffort", "contextWindow")
		}
		sessions = append(sessions, value)
	}
	sort.SliceStable(sessions, func(i, j int) bool { return str(sessions[i], "lastActiveAt") > str(sessions[j], "lastActiveAt") })
	result["sessions"] = sessions
	return result, nil
}

func (s *Server) updateWakerModel(ctx context.Context, identity auth.Identity, b map[string]any) (any, error) {
	if str(b, "confirm") != "/api/waker/models/update" {
		return nil, apierror.New(409, "confirmation_required")
	}
	if err := bodyStrings(b, "wakerId", "projectId", "model", "reasoningEffort", "baseRevision"); err != nil {
		return nil, err
	}
	wid, err := safeID(str(b, "wakerId"))
	if err != nil {
		return nil, err
	}
	model, project := str(b, "model"), str(b, "projectId")
	if strings.TrimSpace(model) == "" || len(model) > 300 || model != strings.TrimSpace(model) || len(project) > 256 || strings.ContainsAny(model+project, "\r\n\x00") {
		return nil, apierror.Invalid("invalid_model_preference")
	}
	release, err := s.writeLock(identity)
	if err != nil {
		return nil, err
	}
	defer release()
	before, err := s.modelPreferences(ctx, wid)
	if err != nil {
		return nil, err
	}
	if !revisionRE.MatchString(str(b, "baseRevision")) || before["revision"] != b["baseRevision"] {
		return nil, apierror.New(409, "version_conflict")
	}
	entry := before["noProject"].(map[string]any)
	if project != "" {
		entry, _ = before["byProject"].(map[string]any)[project].(map[string]any)
	}
	payload := map[string]any{"model": model}
	if project != "" {
		payload["projectId"] = project
	}
	// Keep existing parameters when the caller edits only the model.
	for _, key := range []string{"reasoningEffort", "contextWindow"} {
		if v, ok := entry[key]; ok {
			payload[key] = v
		}
	}
	if value, exists := b["reasoningEffort"]; exists {
		effort, _ := value.(string)
		if effort == "" || len(effort) > 40 || strings.ContainsAny(effort, "\r\n\x00") {
			return nil, apierror.Invalid("invalid_model_preference")
		}
		payload["reasoningEffort"] = effort
	}
	if _, exists := b["contextWindow"]; exists {
		n, e := integer(b, "contextWindow")
		if e != nil || n <= 0 || n > 9007199254740991 {
			return nil, apierror.Invalid("invalid_model_preference")
		}
		payload["contextWindow"] = n
	}
	_, err = s.Daemon.JSON(ctx, "PUT", "/api/model-preferences/"+url.PathEscape(wid)+"/entry", payload)
	s.invalidateState()
	if err != nil {
		var de *daemon.Error
		var ae *apierror.Error
		if errors.As(err, &de) && de.Status >= 400 && de.Status < 500 || errors.As(err, &ae) && ae.Status >= 400 && ae.Status < 500 {
			return nil, err
		}
		return map[string]any{"ok": true, "status": "unknown", "code": modelErrorCode(err)}, nil
	}
	after, err := s.modelPreferences(ctx, wid)
	if err != nil {
		return map[string]any{"ok": true, "status": "saved_unverified", "code": modelErrorCode(err)}, nil
	}
	actual := after["noProject"].(map[string]any)
	if project != "" {
		actual, _ = after["byProject"].(map[string]any)[project].(map[string]any)
	}
	verified := actual["modelId"] == model
	for _, key := range []string{"reasoningEffort", "contextWindow"} {
		if wanted, exists := payload[key]; exists {
			verified = verified && string(jsoncompat.Marshal(actual[key])) == string(jsoncompat.Marshal(wanted))
		}
	}
	status := "verified"
	if !verified {
		status = "readback_mismatch"
	}
	return map[string]any{"ok": true, "status": status, "preferences": after, "revision": after["revision"]}, nil
}
