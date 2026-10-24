package server

import (
	"net/http"
	"os"
	"path/filepath"
	"strconv"

	"qoderwake-panel/internal/apierror"
	"qoderwake-panel/internal/auth"
	"qoderwake-panel/internal/patches"
	contracts "qoderwake-panel/panel/config"
)

func (s *Server) patchManager() *patches.Manager {
	s.patchOnce.Do(func() {
		registry := os.Getenv("QW_PATCH_REGISTRY")
		if registry == "" {
			if _, err := os.Lstat(filepath.Join(s.Config.Root, "patch-registry.json")); err == nil {
				registry = filepath.Join(s.Config.Root, "patch-registry.json")
			}
		}
		port := 19840
		if value, err := strconv.Atoi(os.Getenv("QW_GW_PORT")); err == nil {
			port = value
		}
		s.patches = patches.New(s.Config.Root, s.Config.CLI, registry, contracts.Patches, port)
	})
	return s.patches
}
func (s *Server) patchPost(r *http.Request, id auth.Identity, b map[string]any) (any, error) {
	m := s.patchManager()
	if r.URL.Path == "/api/patch/plan" {
		result, err := m.Plan(str(b, "action"), id.Principal)
		if err != nil {
			return nil, apierror.Invalid(managedCode(err))
		}
		result["ok"] = true
		return result, nil
	}
	if str(b, "confirm") != "/api/patch/execute" {
		return nil, apierror.New(409, "confirmation_required")
	}
	result, err := m.Execute(r.Context(), str(b, "plan"), id.Principal)
	if err != nil {
		return nil, apierror.New(409, managedCode(err))
	}
	s.invalidateState()
	return result, nil
}
