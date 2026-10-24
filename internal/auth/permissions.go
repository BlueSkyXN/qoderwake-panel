package auth

import (
	"qoderwake-panel/internal/apierror"
	"slices"
)

var readRoutes = map[string]string{"/api/state": "", "/api/audit": "", "/api/waker/detail": "id", "/api/waker/models": "waker", "/api/skills": "waker", "/api/skill/content": "waker", "/api/skill/versions": "waker", "/api/skill/diff": "waker", "/api/sessions/query": "waker"}
var writeRoutes = map[string]string{"/api/waker/update": "id", "/api/skill/update": "wakerId", "/api/skill/rollback": "wakerId"}

func Authorize(identity Identity, method, path string, values map[string]any) error {
	if identity.Role == "admin" {
		return nil
	}
	if method == "GET" && path == "/api/maintenance" && identity.Role != "caller" {
		return nil
	}
	if identity.UserID == "" {
		return apierror.New(403, "admin_required")
	}
	if method == "GET" && slices.Contains([]string{"/api/native/catalog", "/api/native/operation", "/api/native/jobs", "/api/native/job"}, path) {
		return nil
	}
	if method == "POST" && slices.Contains([]string{"/api/native/execute", "/api/native/cancel", "/api/account/password"}, path) {
		return nil
	}
	routes := readRoutes
	if method != "GET" {
		routes = map[string]string{}
		if identity.Role == "maintainer" {
			routes = writeRoutes
		}
	}
	field, ok := routes[path]
	if !ok {
		return apierror.New(403, "permission_denied")
	}
	if field != "" {
		value, ok := values[field].(string)
		if !ok || !slices.Contains(identity.Wakers, value) {
			return apierror.New(403, "waker_scope_rejected")
		}
	}
	return nil
}
func Capabilities(identity Identity) []string {
	if identity.Role == "admin" {
		return []string{"admin", "waker.read", "waker.write", "skill.read", "skill.write", "audit.read"}
	}
	if identity.UserID != "" {
		out := []string{"waker.read", "skill.read", "audit.read"}
		if identity.Role == "maintainer" {
			out = append(out, "waker.write", "skill.write")
		}
		return out
	}
	return []string{}
}
