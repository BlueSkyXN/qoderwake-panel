package server

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	"qoderwake-panel/internal/apierror"
	"qoderwake-panel/internal/auth"
	"qoderwake-panel/internal/daemon"
	"qoderwake-panel/internal/jsoncompat"
)

var idRE = regexp.MustCompile(`^[A-Za-z0-9_-]{1,100}$`)
var revisionRE = regexp.MustCompile(`^[a-f0-9]{64}$`)

func safeID(value string) (string, error) {
	if !idRE.MatchString(value) {
		return "", apierror.Invalid("invalid_identifier")
	}
	return value, nil
}
func secureEqual(a, b string) bool { return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1 }
func pick(row map[string]any, keys ...string) map[string]any {
	out := map[string]any{}
	for _, key := range keys {
		out[key] = row[key]
	}
	return out
}
func rows(value any) []map[string]any {
	result := []map[string]any{}
	values, _ := value.([]any)
	for _, v := range values {
		if row, ok := v.(map[string]any); ok {
			result = append(result, row)
		}
	}
	return result
}

func wakerRevision(detail map[string]any) string {
	skills := []map[string]any{}
	for _, s := range rows(detail["skills"]) {
		skills = append(skills, pick(s, "skillId", "enabled"))
	}
	sort.SliceStable(skills, func(i, j int) bool { return str(skills[i], "skillId") < str(skills[j], "skillId") })
	sum := sha256.Sum256(jsoncompat.Marshal(map[string]any{"name": detail["name"], "description": detail["description"], "skills": skills}))
	return hex.EncodeToString(sum[:])
}
func (s *Server) waker(ctx context.Context, wid string) (map[string]any, error) {
	if _, err := safeID(wid); err != nil {
		return nil, err
	}
	raw, err := s.Daemon.JSON(ctx, "GET", "/api/agents/"+wid, nil)
	if err != nil {
		return nil, err
	}
	data, ok := raw["data"].(map[string]any)
	if !ok || (str(data, "agentId") != wid && str(data, "id") != wid) {
		return nil, apierror.Invalid("invalid_waker_detail")
	}
	result := pick(data, "agentId", "id", "name", "description", "sessionTimeout", "skills", "systemIdentity", "revision", "version", "updatedAt", "updated_at")
	result["configRevision"] = wakerRevision(result)
	return result, nil
}
func (s *Server) skills(ctx context.Context, wid string) ([]map[string]any, error) {
	detail, err := s.waker(ctx, wid)
	if err != nil {
		return nil, err
	}
	result := []map[string]any{}
	for _, row := range rows(detail["skills"]) {
		result = append(result, pick(row, "skillId", "name", "description", "enabled", "source", "mutableByAgent", "pinned", "currentVersionId"))
	}
	return result, nil
}
func (s *Server) skill(ctx context.Context, wid, sid string) (map[string]any, error) {
	if _, err := safeID(sid); err != nil {
		return nil, err
	}
	list, err := s.skills(ctx, wid)
	if err != nil {
		return nil, err
	}
	var metadata map[string]any
	for _, row := range list {
		if row["skillId"] == sid {
			metadata = row
			break
		}
	}
	if metadata == nil {
		return nil, apierror.New(404, "skill_not_found")
	}
	raw, err := s.Daemon.JSON(ctx, "GET", "/api/agents/"+wid+"/skills/"+sid+"/content", nil)
	if err != nil {
		if e, ok := err.(*daemon.Error); ok && e.Status == 404 {
			return map[string]any{"skill": metadata, "content": "", "editable": false, "contentAvailable": false, "readOnlyReason": "skill_content_unavailable"}, nil
		}
		return nil, err
	}
	data := daemon.Data(raw)
	if added, ok := data["skill"].(map[string]any); ok {
		for k, v := range added {
			metadata[k] = v
		}
	}
	content, available := data["content"].(string)
	editable := metadata["mutableByAgent"] == true && metadata["pinned"] != true && str(metadata, "currentVersionId") != ""
	var reason any
	if !editable {
		reason = "skill_read_only"
	}
	return map[string]any{"skill": pick(metadata, "skillId", "name", "description", "enabled", "source", "mutableByAgent", "pinned", "currentVersionId", "updatedAt"), "content": content, "contentAvailable": available, "editable": editable, "readOnlyReason": reason}, nil
}
func (s *Server) skillVersions(ctx context.Context, wid, sid string) ([]map[string]any, error) {
	if _, err := s.skill(ctx, wid, sid); err != nil {
		return nil, err
	}
	raw, err := s.Daemon.JSON(ctx, "GET", "/api/agents/"+wid+"/skills/"+sid+"/versions", nil)
	if err != nil {
		return nil, err
	}
	value := raw["data"]
	if obj, ok := value.(map[string]any); ok {
		value = obj["items"]
		if value == nil {
			value = obj["versions"]
		}
	}
	out := []map[string]any{}
	for _, v := range rows(value) {
		out = append(out, pick(v, "versionId", "createdAt", "updatedAt", "reason", "source"))
	}
	return out, nil
}
func (s *Server) skillDiff(ctx context.Context, wid, sid, version string) (map[string]any, error) {
	if _, err := s.skill(ctx, wid, sid); err != nil {
		return nil, err
	}
	if version == "" || len(version) > 300 || strings.ContainsAny(version, "\r\n\x00") {
		return nil, apierror.Invalid("invalid_identifier")
	}
	raw, err := s.Daemon.JSON(ctx, "GET", "/api/agents/"+wid+"/skills/"+sid+"/versions/"+url.PathEscape(version)+"/diff", nil)
	if err != nil {
		return nil, err
	}
	result := daemon.Data(raw)
	if len(jsoncompat.Marshal(result)) > 256*1024 {
		return nil, apierror.Invalid("skill_diff_too_large")
	}
	return result, nil
}
func (s *Server) state(ctx context.Context, identity auth.Identity) (map[string]any, error) {
	state := map[string]any{"version": Version, "daemon": "stopped", "models": []map[string]any{}, "wakers": []map[string]any{}, "providers": []map[string]any{}, "whoami": nil, "providerRevision": nil, "pluginWritesEnabled": false, "whitelist": []any{}}
	models, err := s.Daemon.JSON(ctx, "GET", "/api/models", nil)
	if err != nil {
		state["error"] = "daemon_unavailable"
		if e, ok := err.(*apierror.Error); ok && strings.HasPrefix(e.Code, "daemon_credential_") {
			state["error"] = e.Code
		}
		return state, nil
	}
	state["daemon"] = "running"
	modelRows := []map[string]any{}
	for _, m := range rows(daemon.Data(models)["models"]) {
		name := m["name"]
		if name == nil || name == "" {
			name = m["displayName"]
		}
		modelRows = append(modelRows, map[string]any{"id": m["id"], "name": name, "provider": m["provider"]})
	}
	if identity.UserID == "" || identity.Role == "admin" {
		state["models"] = modelRows
	}
	raw, err := s.Daemon.JSON(ctx, "GET", "/api/agents", nil)
	if err != nil {
		state["error"] = "daemon_unavailable"
		return state, nil
	}
	value := raw["data"]
	if value == nil {
		value = raw
	}
	if obj, ok := value.(map[string]any); ok {
		value = obj["list"]
	}
	wakers := []map[string]any{}
	for _, row := range rows(value) {
		wid := str(row, "agentId")
		if identity.UserID != "" && identity.Role != "admin" && !contains(identity.Wakers, wid) {
			continue
		}
		wakers = append(wakers, map[string]any{"id": wid, "name": row["name"], "preference": "(未知)"})
	}
	var wg sync.WaitGroup
	work := make(chan map[string]any)
	for range min(4, len(wakers)) {
		wg.Go(func() {
			for row := range work {
				row["preference"] = s.wakerPreference(ctx, str(row, "id"))
			}
		})
	}
	for _, row := range wakers {
		work <- row
	}
	close(work)
	wg.Wait()
	state["wakers"] = wakers
	if identity.Role == "admin" {
		if raw, e := s.Daemon.JSON(ctx, "GET", "/api/frontend-auth/me", nil); e == nil {
			value := daemon.Data(raw)
			profile, _ := value["profile"].(map[string]any)
			if value["authenticated"] == true {
				if name, ok := profile["name"].(string); ok {
					state["whoami"] = map[string]any{"name": string([]rune(name)[:min(200, len([]rune(name)))])}
				}
			}
		}
	}
	return state, nil
}
func (s *Server) writeResource(ctx context.Context, identity auth.Identity, path string, b map[string]any) (any, error) {
	if path != "/api/waker/update" && str(b, "confirm") != path {
		return nil, apierror.New(409, "confirmation_required")
	}
	if !s.resources.TryLock() {
		return nil, apierror.New(409, "resource_mutation_in_progress")
	}
	defer s.resources.Unlock()
	var err error
	identity, err = s.Auth.Refresh(identity)
	if err != nil {
		return nil, err
	}
	if err = auth.Authorize(identity, "POST", path, b); err != nil {
		return nil, err
	}
	blocked, err := s.Auth.HasUnresolvedDeletions()
	if err != nil {
		return nil, err
	}
	if blocked {
		return nil, apierror.New(409, "deletion_operation_unresolved")
	}
	if path == "/api/waker/update" {
		wid := str(b, "id")
		current, err := s.waker(ctx, wid)
		if err != nil {
			return nil, err
		}
		base := str(b, "baseRevision")
		if !revisionRE.MatchString(base) || !secureEqual(str(current, "configRevision"), base) {
			return nil, apierror.New(409, "version_conflict")
		}
		name, description := strings.TrimSpace(str(b, "name")), str(b, "description")
		if name == "" || utf8.RuneCountInString(name) > 100 || utf8.RuneCountInString(description) > 4000 {
			return nil, apierror.Invalid("invalid_text")
		}
		skills, ok := b["skills"].([]any)
		if !ok || len(skills) > 200 {
			return nil, apierror.Invalid("invalid_skills")
		}
		allowed := map[string]bool{}
		for _, row := range rows(current["skills"]) {
			allowed[str(row, "skillId")] = true
		}
		bodySkills := []map[string]any{}
		seen := map[string]bool{}
		for _, entry := range skills {
			row, ok := entry.(map[string]any)
			if !ok {
				return nil, apierror.Invalid("invalid_skills")
			}
			sid := str(row, "skillId")
			enabled, ok := row["enabled"].(bool)
			if !ok || !allowed[sid] || seen[sid] {
				return nil, apierror.Invalid("invalid_skills")
			}
			seen[sid] = true
			bodySkills = append(bodySkills, map[string]any{"skillId": sid, "enabled": enabled})
		}
		if _, err = s.Daemon.JSON(ctx, "PUT", "/api/agents/"+wid, map[string]any{"name": name, "description": description, "skills": bodySkills}); err != nil {
			return nil, err
		}
		var revision any
		if next, e := s.waker(ctx, wid); e == nil {
			revision = next["configRevision"]
		}
		return map[string]any{"ok": true, "revision": revision}, nil
	}
	wid, sid := str(b, "wakerId"), str(b, "skillId")
	current, err := s.skill(ctx, wid, sid)
	if err != nil {
		return nil, err
	}
	if current["editable"] != true || current["contentAvailable"] != true {
		return nil, apierror.New(403, "skill_read_only")
	}
	meta, _ := current["skill"].(map[string]any)
	base := str(b, "baseVersionId")
	if base == "" || base != str(meta, "currentVersionId") {
		return nil, apierror.New(409, "version_conflict")
	}
	body := map[string]any{"baseVersionId": base}
	endpoint := "/api/agents/" + wid + "/skills/" + sid
	if path == "/api/skill/update" {
		content, ok := b["content"].(string)
		if !ok || len(content) > 60000 {
			return nil, apierror.Invalid("invalid_skill_content")
		}
		body["content"] = content
		endpoint += "/content"
	} else {
		version, reason := str(b, "versionId"), strings.TrimSpace(str(b, "reason"))
		if version == "" || len(version) > 300 || reason == "" || utf8.RuneCountInString(reason) > 500 {
			return nil, apierror.Invalid("invalid_request")
		}
		body["versionId"] = version
		body["reason"] = reason
		versions, e := s.skillVersions(ctx, wid, sid)
		if e != nil {
			return nil, e
		}
		found := false
		for _, row := range versions {
			found = found || row["versionId"] == version
		}
		if !found {
			return nil, apierror.Invalid("skill_version_not_found")
		}
		delete(body, "baseVersionId")
		endpoint += "/rollback"
	}
	method := "PUT"
	if path == "/api/skill/rollback" {
		method = "POST"
	}
	_, err = s.Daemon.JSON(ctx, method, endpoint, body)
	if err != nil {
		return nil, err
	}
	var revision any
	if next, e := s.skill(ctx, wid, sid); e == nil {
		if metadata, ok := next["skill"].(map[string]any); ok {
			revision = metadata["currentVersionId"]
		}
	}
	return map[string]any{"ok": true, "revision": revision}, nil
}
