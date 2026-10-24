package server

import (
	"context"
	"errors"
	"strings"

	"qoderwake-panel/internal/apierror"
	"qoderwake-panel/internal/auth"
	"qoderwake-panel/internal/daemon"
	"qoderwake-panel/internal/deletion"
	"qoderwake-panel/internal/provider"
)

func deletionTarget(kind, target string) (string, error) {
	switch kind {
	case "waker":
		return safeID(target)
	case "provider":
		target = strings.TrimSpace(target)
		if target == "" || len([]rune(target)) > 120 {
			return "", apierror.Invalid("invalid_text")
		}
		return target, nil
	}
	return "", apierror.Invalid("invalid_deletion_kind")
}
func (s *Server) deletePost(ctx context.Context, identity auth.Identity, path, key string, b map[string]any) (any, error) {
	if err := bodyStrings(b, "kind", "target", "name", "id", "baseRevision", "idempotencyKey", "preview", "impactDigest", "decision", "targetConfirmation"); err != nil {
		return nil, err
	}
	if path == "/api/deletion/status" {
		operation, err := s.Deletions.Operation(identity.Principal, str(b, "idempotencyKey"))
		return struct {
			OK bool `json:"ok"`
			deletion.Operation
		}{true, operation}, err
	}
	kind, target := str(b, "kind"), str(b, "target")
	if path == "/api/waker/delete" {
		kind, target = "waker", str(b, "id")
	}
	if path == "/api/provider/delete" {
		kind, target = "provider", str(b, "name")
	}
	target, err := deletionTarget(kind, target)
	if err != nil {
		return nil, err
	}
	if path == "/api/deletion/preview" {
		revision := ""
		if kind == "provider" {
			revision = str(b, "baseRevision")
			if err = provider.RequireRevision(revision); err != nil {
				return nil, err
			}
			snapshot, e := s.Providers.Read()
			if e != nil {
				return nil, e
			}
			if snapshot.Revision != revision {
				return nil, apierror.New(409, "version_conflict")
			}
		}
		if status, e := s.Deletions.Unresolved(kind, target); e != nil {
			return nil, e
		} else if status != "" {
			return nil, apierror.New(409, "deletion_operation_unresolved")
		}
		impact, e := (deletion.Scanner{Source: s.deletionSource(ctx)}).Scan(kind, target, revision)
		if e != nil {
			return nil, e
		}
		var id, expires any
		if impact.Deletable {
			raw, expiry, e := s.Deletions.Create(identity.Principal, impact)
			if e != nil {
				return nil, e
			}
			id, expires = raw, expiry
		}
		return struct {
			OK bool `json:"ok"`
			deletion.Impact
			ID      any `json:"id"`
			Expires any `json:"expires"`
		}{true, impact, id, expires}, nil
	}
	if !s.resources.TryLock() {
		return nil, apierror.New(409, "resource_mutation_in_progress")
	}
	defer s.resources.Unlock()
	if identity, err = s.Auth.Refresh(identity); err != nil {
		return nil, err
	}
	if identity.Role != "admin" {
		return nil, apierror.New(403, "admin_required")
	}
	switch path {
	case "/api/deletion/reconcile":
		exists := true
		if kind == "waker" {
			_, e := s.waker(ctx, target)
			if e != nil {
				var de *daemon.Error
				var ae *apierror.Error
				if errors.As(e, &de) && de.Status == 404 || errors.As(e, &ae) && ae.Status == 404 {
					exists = false
				} else {
					return nil, e
				}
			}
		} else {
			snapshot, e := s.Providers.Read()
			if e != nil {
				return nil, e
			}
			_, exists = snapshot.Data["providers"].(map[string]any)[target]
		}
		status, e := s.Deletions.Unresolved(kind, target)
		if e != nil {
			return nil, e
		}
		if exists {
			if status == "" {
				return nil, apierror.New(404, "deletion_operation_not_found")
			}
			return map[string]any{"ok": true, "status": status, "error": "deletion_target_still_exists_unresolved", "reconciled": false}, nil
		}
		result := map[string]any{"ok": true, "status": "succeeded", "reconciled": true}
		if status != "" {
			if e = s.Deletions.Resolve(kind, target, "succeeded", result); e != nil {
				return nil, e
			}
		}
		return result, nil
	case "/api/deletion/release":
		if str(b, "confirm") != path {
			return nil, apierror.New(409, "confirmation_required")
		}
		if str(b, "decision") != "failed" || str(b, "targetConfirmation") != target {
			return nil, apierror.Invalid("deletion_release_confirmation_required")
		}
		stored := map[string]any{"ok": false, "status": "failed", "error": "deletion_released_by_admin", "released": true}
		if err = s.Deletions.Resolve(kind, target, "failed", stored); err != nil {
			return nil, err
		}
		return map[string]any{"ok": true, "status": "failed", "released": true}, nil
	case "/api/waker/delete", "/api/provider/delete":
		if str(b, "confirm") != path {
			return nil, apierror.New(409, "confirmation_required")
		}
		return s.executeDeletion(ctx, identity.Principal, kind, target, key, b)
	}
	return nil, apierror.New(404, "not_found")
}
func (s *Server) executeDeletion(ctx context.Context, principal, kind, target, key string, b map[string]any) (map[string]any, error) {
	warnings, err := stringList(b, "acknowledgedWarnings")
	if err != nil {
		return nil, apierror.Invalid("invalid_deletion_warnings")
	}
	impactDigest := str(b, "impactDigest")
	previous, err := s.Deletions.Replay(principal, key, kind, target, impactDigest, warnings)
	if err != nil || previous != nil {
		return previous, err
	}
	revision := ""
	if kind == "provider" {
		revision = str(b, "baseRevision")
		if err = provider.RequireRevision(revision); err != nil {
			return nil, err
		}
		snapshot, e := s.Providers.Read()
		if e != nil {
			return nil, e
		}
		if snapshot.Revision != revision {
			return nil, apierror.New(409, "stale_deletion_preview")
		}
	}
	stored, err := s.Deletions.Preview(str(b, "preview"), principal, kind, target)
	if err != nil {
		return nil, err
	}
	if kind == "provider" && revision != stored.Revision {
		return nil, apierror.New(409, "stale_deletion_preview")
	}
	fresh, err := (deletion.Scanner{Source: s.deletionSource(ctx)}).Scan(kind, target, revision)
	if err != nil {
		return nil, err
	}
	if !fresh.Deletable || fresh.Revision != stored.Revision || fresh.ImpactDigest != stored.ImpactDigest || fresh.ImpactDigest != impactDigest {
		return nil, apierror.New(409, "stale_deletion_preview")
	}
	previous, err = s.Deletions.Begin(str(b, "preview"), principal, kind, target, impactDigest, warnings, key)
	if err != nil || previous != nil {
		return previous, err
	}
	result := map[string]any{"ok": true, "status": "succeeded"}
	if kind == "provider" {
		snapshot, e := s.Providers.Delete(target, revision)
		err = e
		if e == nil {
			result["providerRevision"] = snapshot.Revision
		}
	} else {
		_, err = s.Daemon.JSON(ctx, "DELETE", "/api/agents/"+target, nil)
	}
	if err != nil {
		status, code := 0, ""
		var de *daemon.Error
		var ae *apierror.Error
		if errors.As(err, &de) {
			status, code = de.Status, de.Code
		} else if errors.As(err, &ae) {
			status, code = ae.Status, ae.Code
		}
		result = map[string]any{"ok": false, "status": "unknown", "error": "deletion_result_unknown"}
		switch {
		case status == 202:
			result["status"] = "pending"
			result["error"] = "deletion_operation_pending"
		case status >= 400 && status < 500:
			result["status"] = "failed"
			result["error"] = code
		}
		if e := s.Deletions.Finish(principal, key, result["status"].(string), result); e != nil {
			return nil, e
		}
		if result["status"] == "failed" {
			return nil, err
		}
		return result, nil
	}
	if err = s.Deletions.Finish(principal, key, "succeeded", result); err != nil {
		return nil, err
	}
	return result, nil
}
