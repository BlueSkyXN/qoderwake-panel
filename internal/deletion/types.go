// Package deletion performs bounded dependency scans before destructive actions.
package deletion

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"qoderwake-panel/internal/jsoncompat"
)

type Source func(name, target string) (any, error)
type Check struct {
	ID           string   `json:"id"`
	Label        string   `json:"label"`
	Status       string   `json:"status"`
	Completeness string   `json:"completeness"`
	Required     bool     `json:"required"`
	Count        int      `json:"count"`
	Refs         []string `json:"refs"`
	Note         string   `json:"note"`
}
type Impact struct {
	Kind             string   `json:"kind"`
	Target           string   `json:"target"`
	Revision         string   `json:"revision"`
	ImpactDigest     string   `json:"impactDigest"`
	Checks           []Check  `json:"checks"`
	WarningsRequired []string `json:"warningsRequired"`
	Deletable        bool     `json:"deletable"`
	Completeness     string   `json:"completeness"`
}
type Scanner struct{ Source Source }
type stringSet map[string]bool

var targetKeys = set("wakerId", "agentId", "workerId", "targetId", "target_key", "resource_locator", "wakerKey", "bindingTargetId")
var containers = set("target", "targets", "executionTarget", "execution_target", "bindingTarget", "conversations", "conversation", "members", "leader")
var modelKeys = set("model", "currentModel", "current_model", "modelId", "model_id")
var activeStates = set("open", "running", "queued", "pending", "starting", "needs_restart", "processing", "in_progress")
var terminalStates = set("success", "succeeded", "completed", "failed", "canceled", "cancelled", "terminated", "closed", "idle")
var unavailable = errors.New("dependency_source_unavailable")

func set(values ...string) stringSet {
	out := stringSet{}
	for _, v := range values {
		out[v] = true
	}
	return out
}
func text(value any) string           { s, _ := value.(string); return strings.TrimSpace(s) }
func clip(value string, n int) string { r := []rune(value); return string(r[:min(n, len(r))]) }
func object(value any) (map[string]any, bool) {
	m, ok := value.(map[string]any)
	return m, ok && m != nil
}
func list(value any) ([]any, bool) { rows, ok := value.([]any); return rows, ok && rows != nil }
func sortedSet(values []string) []string {
	found := set(values...)
	out := []string{}
	for v := range found {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}
func model(value any) (string, error) {
	if value == nil {
		return "", nil
	}
	if s, ok := value.(string); ok {
		return strings.TrimSpace(s), nil
	}
	if row, ok := object(value); ok {
		values := []string{}
		for _, key := range []string{"modelId", "model_id", "model", "id"} {
			if value, exists := row[key]; exists {
				v := text(value)
				if v == "" {
					return "", unavailable
				}
				values = append(values, v)
			}
		}
		unique := sortedSet(values)
		if len(unique) == 1 {
			return unique[0], nil
		}
	}
	return "", unavailable
}
func targets(value any) (stringSet, error) {
	out := set()
	switch v := value.(type) {
	case []any:
		for _, item := range v {
			next, e := targets(item)
			if e != nil {
				return nil, e
			}
			for key := range next {
				out[key] = true
			}
		}
	case map[string]any:
		for key, item := range v {
			if targetKeys[key] {
				s := text(item)
				if s == "" {
					return nil, unavailable
				}
				out[strings.TrimPrefix(s, "local:")] = true
			} else if containers[key] && item != nil {
				next, e := targets(item)
				if e != nil {
					return nil, e
				}
				for key := range next {
					out[key] = true
				}
			}
		}
	case string:
		if strings.TrimSpace(v) == "" {
			return nil, unavailable
		}
		out[strings.TrimPrefix(strings.TrimSpace(v), "local:")] = true
	default:
		return nil, unavailable
	}
	return out, nil
}
func singleTarget(value any) string {
	values, e := targets(value)
	if e != nil || len(values) != 1 {
		return ""
	}
	for v := range values {
		return v
	}
	return ""
}
func models(value any) (stringSet, error) {
	out := set()
	switch v := value.(type) {
	case []any:
		for _, item := range v {
			next, e := models(item)
			if e != nil {
				return nil, e
			}
			for k := range next {
				out[k] = true
			}
		}
	case map[string]any:
		for k, item := range v {
			if modelKeys[k] {
				name, e := model(item)
				if e != nil {
					return nil, e
				}
				if name != "" {
					out[name] = true
				}
			} else if containers[k] || k == "config" || k == "channel" {
				next, e := models(item)
				if e != nil {
					return nil, e
				}
				for k := range next {
					out[k] = true
				}
			}
		}
	case nil, string:
	default:
		return nil, unavailable
	}
	return out, nil
}
func channelPayload(value any) (map[string]any, error) {
	row, ok := object(value)
	if !ok {
		return nil, unavailable
	}
	if v, exists := row["channel"]; exists {
		row, ok = object(v)
		if !ok {
			return nil, unavailable
		}
	}
	if v, exists := row["config"]; exists {
		row, ok = object(v)
		if !ok {
			return nil, unavailable
		}
	}
	return row, nil
}
func channelTargets(value any) (stringSet, error) {
	cfg, e := channelPayload(value)
	if e != nil {
		return nil, e
	}
	out := set()
	for _, key := range []string{"agentId", "wakerId", "bindingTargetId"} {
		if value, exists := cfg[key]; exists {
			s := strings.TrimPrefix(text(value), "local:")
			if s == "" {
				return nil, unavailable
			}
			out[s] = true
		}
	}
	if binding, exists := cfg["bindingTarget"]; exists && binding != nil {
		if value, ok := binding.(string); ok {
			s := strings.TrimPrefix(strings.TrimSpace(value), "local:")
			if s == "" {
				return nil, unavailable
			}
			out[s] = true
		} else if _, ok := object(binding); ok {
			values, e := targets(binding)
			if e != nil || len(values) != 1 {
				return nil, unavailable
			}
			for v := range values {
				out[v] = true
			}
		} else {
			return nil, unavailable
		}
	}
	return out, nil
}
func sessionState(value any) (string, error) {
	row, ok := object(value)
	if !ok {
		return "", unavailable
	}
	raw, exists := row["session_status"]
	if !exists {
		raw = row["status"]
	}
	state := strings.ToLower(text(raw))
	if activeStates[state] {
		return "active", nil
	}
	if terminalStates[state] {
		return "terminal", nil
	}
	return "", unavailable
}
func groupParts(value any) (map[string]any, []any, any, error) {
	row, ok := object(value)
	if !ok {
		return nil, nil, nil, unavailable
	}
	group := row
	if v, exists := row["group"]; exists {
		group, ok = object(v)
		if !ok {
			return nil, nil, nil, unavailable
		}
	}
	members, exists := row["members"]
	if !exists {
		members = group["members"]
	}
	leader, exists := row["leader"]
	if !exists {
		leader = group["leader"]
	}
	items, ok := list(members)
	if !ok || leader == nil {
		return nil, nil, nil, unavailable
	}
	return group, items, leader, nil
}
func memberID(value any) string {
	if s, ok := value.(string); ok {
		return strings.TrimPrefix(strings.TrimSpace(s), "local:")
	}
	row, ok := object(value)
	if !ok {
		return ""
	}
	target := map[string]any{}
	if v, exists := row["target"]; exists {
		target, ok = object(v)
		if !ok {
			return ""
		}
	}
	values := []string{}
	for _, item := range []struct {
		row  map[string]any
		keys []string
	}{{row, []string{"wakerKey", "wakerId", "agentId"}}, {target, []string{"agentId", "wakerId", "id"}}} {
		for _, key := range item.keys {
			if v, exists := item.row[key]; exists {
				value := strings.TrimPrefix(text(v), "local:")
				if value == "" {
					return ""
				}
				values = append(values, value)
			}
		}
	}
	if len(values) == 0 {
		values = append(values, strings.TrimPrefix(text(row["id"]), "local:"))
	}
	unique := sortedSet(values)
	if len(unique) == 1 {
		return unique[0]
	}
	return ""
}
func validateGroupModels(value any) error {
	switch v := value.(type) {
	case map[string]any:
		for key, item := range v {
			if strings.Contains(strings.ToLower(key), "model") && !modelKeys[key] {
				return unavailable
			}
			if containers[key] || key == "config" || key == "channel" {
				if e := validateGroupModels(item); e != nil {
					return e
				}
			}
		}
	case []any:
		for _, item := range v {
			if e := validateGroupModels(item); e != nil {
				return e
			}
		}
	}
	return nil
}
func memberModels(value any) (stringSet, error) {
	if memberID(value) == "" {
		return nil, unavailable
	}
	if e := validateGroupModels(value); e != nil {
		return nil, e
	}
	return models(value)
}
func preferences(value any) (stringSet, error) {
	row, ok := object(value)
	if !ok {
		return nil, unavailable
	}
	_, one := row["noProject"]
	_, many := row["byProject"]
	if !one && !many {
		return nil, unavailable
	}
	out := set()
	if one {
		name, e := model(row["noProject"])
		if e != nil {
			return nil, e
		}
		if name != "" {
			out[name] = true
		}
	}
	projects := map[string]any{}
	if many {
		projects, ok = object(row["byProject"])
		if !ok {
			return nil, unavailable
		}
	}
	for _, v := range projects {
		name, e := model(v)
		if e != nil {
			return nil, e
		}
		if name != "" {
			out[name] = true
		}
	}
	return out, nil
}
func intersects(a, b stringSet) bool {
	for k := range a {
		if b[k] {
			return true
		}
	}
	return false
}
func refs(values []any, keys ...string) []string {
	out := []string{}
	seen := set()
	for _, value := range values {
		v := ""
		if s, ok := value.(string); ok {
			v = s
		} else if row, ok := object(value); ok {
			for _, key := range keys {
				if text(row[key]) != "" {
					v = text(row[key])
					break
				}
			}
		}
		if v != "" && !seen[v] {
			seen[v] = true
			out = append(out, clip(v, 160))
		}
		if len(out) >= 40 {
			break
		}
	}
	sort.Strings(out)
	return out
}
func check(id, label, status string, required bool, count int, references []string, note string) Check {
	references = sortedSet(references)
	return Check{id, label, status, "complete", required, count, references[:min(40, len(references))], clip(note, 400)}
}
func unknown(id, label string, required bool) Check {
	c := check(id, label, "unknown", required, 0, nil, "无法完整读取该依赖源")
	c.Completeness = "unavailable"
	return c
}
func matching(id, label, hitStatus string, required bool, matched []any, keys ...string) Check {
	status := "clear"
	if len(matched) > 0 {
		status = hitStatus
	}
	return check(id, label, status, required, len(matched), refs(matched, keys...), "")
}
func digest(value any) string {
	raw := jsoncompat.Marshal(value)
	var compact bytes.Buffer
	json.Compact(&compact, raw)
	sum := sha256.Sum256(compact.Bytes())
	return hex.EncodeToString(sum[:])
}
func fingerprint(value any) (string, error) {
	row, ok := object(value)
	if !ok {
		return "", unavailable
	}
	data := map[string]any{}
	for _, key := range []string{"agentId", "id", "name", "description", "sessionTimeout", "revision", "version", "updatedAt", "updated_at", "skills"} {
		data[key] = row[key]
	}
	return digest(data), nil
}
func canonical(kind, target, revision string, checks []Check) string {
	ordered := append([]Check{}, checks...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].ID < ordered[j].ID })
	items := []map[string]any{}
	for _, c := range ordered {
		items = append(items, map[string]any{"id": c.ID, "status": c.Status, "completeness": c.Completeness, "required": c.Required, "count": c.Count, "refs": c.Refs})
	}
	return digest(map[string]any{"kind": kind, "target": target, "revision": revision, "checks": items})
}
func (s Scanner) Scan(kind, target, revision string) (Impact, error) {
	var checks []Check
	switch kind {
	case "waker":
		var current string
		checks, current = s.waker(target)
		if revision == "" {
			revision = current
		}
	case "provider":
		checks = s.provider(target)
	default:
		return Impact{}, fmt.Errorf("invalid_deletion_kind")
	}
	result := Impact{Kind: kind, Target: target, Revision: revision, Checks: checks, WarningsRequired: []string{}, Deletable: true, Completeness: "complete"}
	for _, c := range checks {
		if c.Required && (c.Status == "blocked" || c.Status == "unknown" || c.Completeness != "complete") {
			result.Deletable = false
		}
		if c.Status == "warning" {
			result.WarningsRequired = append(result.WarningsRequired, c.ID)
		}
		if c.Required && c.Completeness == "unavailable" {
			result.Completeness = "unavailable"
		} else if result.Completeness != "unavailable" && c.Completeness != "complete" {
			result.Completeness = "partial"
		}
	}
	result.ImpactDigest = canonical(kind, target, revision, checks)
	return result, nil
}
