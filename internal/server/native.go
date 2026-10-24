package server

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"qoderwake-panel/internal/apierror"
	"qoderwake-panel/internal/auth"
	"qoderwake-panel/internal/cli"
	"qoderwake-panel/internal/daemon"
)

func (s *Server) nativeGuard(ctx context.Context, identity auth.Identity, spec cli.Spec, req cli.Request) (func(), error) {
	identity, err := s.Auth.Refresh(identity)
	if err != nil {
		return nil, err
	}
	if err = s.Catalog.Authorize(identity, spec, &req.Parameters); err != nil {
		return nil, err
	}
	target, err := url.Parse(s.Config.DaemonURL)
	if err != nil || !slices.Contains([]string{"127.0.0.1", "localhost", "::1"}, target.Hostname()) {
		return nil, apierror.New(409, "cli_instance_mismatch")
	}
	if value, e := os.ReadFile(filepath.Join(s.Config.Home, "run/daemon.port")); e == nil {
		if len(value) > 32 || strings.TrimSpace(string(value)) != target.Port() {
			return nil, apierror.New(409, "cli_instance_mismatch")
		}
	} else if !os.IsNotExist(e) {
		return nil, e
	}
	if spec.Effect == "read" {
		return func() {}, nil
	}
	lease, err := s.Admission.Acquire(true)
	if err != nil {
		return nil, err
	}
	if !s.resources.TryLock() {
		lease.Release()
		return nil, apierror.New(409, "resource_mutation_in_progress")
	}
	var epoch uint64
	release := func() {
		if epoch > 0 {
			s.Admission.Complete(epoch)
		}
		s.resources.Unlock()
		lease.Release()
	}
	fail := func(e error) (func(), error) { release(); return nil, e }
	blocked, err := s.Auth.HasUnresolvedDeletions()
	if err != nil {
		return fail(err)
	}
	if blocked {
		return fail(apierror.New(409, "deletion_operation_unresolved"))
	}
	options := req.Parameters.Options
	wid, _ := options["--waker-id"].(string)
	if strings.HasPrefix(spec.ID, "waker.update.") {
		current, e := s.waker(ctx, wid)
		if e != nil {
			return fail(e)
		}
		if str(req.Raw, "baseRevision") != str(current, "configRevision") {
			return fail(apierror.New(409, "version_conflict"))
		}
	}
	if slices.Contains([]string{"skill.manage", "skill.delete", "skill.self-evolution"}, spec.ID) {
		sid, _ := options["--skill-id"].(string)
		if sid != "" && !(spec.ID == "skill.manage" && options["--action"] == "create") {
			base, _ := options["--base-version-id"].(string)
			if base == "" {
				base = str(req.Raw, "baseVersionId")
			}
			if base == "" {
				return fail(apierror.New(409, "version_conflict"))
			}
			conversation, _ := options["--conversation-id"].(string)
			if spec.ID == "skill.manage" && conversation != "" && wid == "" {
				if len(conversation) > 300 || strings.ContainsAny(conversation, "\x00\r\n") || options["--base-version-id"] != base {
					return fail(apierror.New(409, "version_conflict"))
				}
			} else {
				current, e := s.skill(ctx, wid, sid)
				if e != nil {
					return fail(e)
				}
				metadata, _ := current["skill"].(map[string]any)
				if str(metadata, "currentVersionId") != base {
					return fail(apierror.New(409, "version_conflict"))
				}
				if spec.ID == "skill.manage" && (current["editable"] != true || current["contentAvailable"] != true) {
					return fail(apierror.New(403, "skill_read_only"))
				}
			}
		}
	}
	if spec.ID == "stop" || spec.ID == "restart" {
		epoch, err = s.Admission.Begin("官方 CLI "+spec.ID, 5*time.Minute, lease)
		if err != nil {
			return fail(err)
		}
		drain, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		if err = s.Admission.Wait(drain, epoch); err != nil {
			return fail(err)
		}
		if err = s.waitDaemonIdle(drain); err != nil {
			return fail(err)
		}
		if err = s.Admission.Transition(epoch, "restarting"); err != nil {
			return fail(err)
		}
	}
	return release, nil
}
func (s *Server) systemStatus(ctx context.Context) (map[string]any, error) {
	health, err := s.Daemon.JSON(ctx, "GET", "/api/health", nil)
	if err != nil {
		return nil, err
	}
	raw, err := s.Daemon.JSON(ctx, "GET", "/api/v1/system/status", nil)
	if err != nil {
		return nil, err
	}
	status := daemon.Data(raw)
	update, _ := status["update"].(map[string]any)
	activity, _ := status["activity"].(map[string]any)
	version := status["version"]
	if version == nil {
		version = daemon.Data(health)["version"]
	}
	return map[string]any{"version": version, "update": pick(update, "runningVersion", "installedVersion", "restartRequired", "showUpdateDiagnostics"), "activity": pick(activity, "runningSessions", "runningConversationSessions", "runningConversationTasks", "runningTriggerSessions", "runningTriggerTasks", "queuedTriggerTasks", "hasRunningWork"), "timestamp": status["timestamp"]}, nil
}
func (s *Server) waitDaemonIdle(ctx context.Context) error {
	consecutive := 0
	for {
		system, err := s.systemStatus(ctx)
		if err != nil {
			return apierror.New(503, "daemon_activity_unknown")
		}
		activity, _ := system["activity"].(map[string]any)
		running, ok := activity["hasRunningWork"].(bool)
		if !ok {
			return apierror.New(503, "daemon_activity_unknown")
		}
		clear := !running
		for _, key := range []string{"runningSessions", "runningConversationSessions", "runningConversationTasks", "runningTriggerSessions", "runningTriggerTasks", "queuedTriggerTasks"} {
			value, e := number(activity[key])
			if e != nil || value < 0 {
				return apierror.New(503, "daemon_activity_unknown")
			}
			clear = clear && value == 0
		}
		if clear {
			consecutive++
			if consecutive >= 2 {
				return nil
			}
		} else {
			consecutive = 0
		}
		select {
		case <-ctx.Done():
			return apierror.New(503, "daemon_activity_not_idle")
		case <-time.After(250 * time.Millisecond):
		}
	}
}
func (s *Server) nativeGet(ctx context.Context, id auth.Identity, path string, b map[string]any) (any, error) {
	switch path {
	case "/api/native/catalog":
		return s.Catalog.Listing(id), nil
	case "/api/native/operation":
		detail, err := s.Jobs.Detail(ctx, id, str(b, "id"))
		if err != nil {
			return nil, err
		}
		contextKeys := []string{}
		if id.Role == "admin" {
			contextKeys = cli.ContextKeys
		} else {
			options := []cli.Option{}
			for _, option := range detail.Options {
				if slices.Contains(s.Catalog.Policy.Scoped[detail.ID], option.Name) {
					options = append(options, option)
				}
			}
			detail.Options = options
		}
		return struct {
			cli.Detail
			ContextKeys []string `json:"contextKeys"`
		}{detail, contextKeys}, nil
	case "/api/native/jobs":
		items, err := s.Jobs.List(id)
		return map[string]any{"items": items}, err
	case "/api/native/job":
		offset := int64(0)
		if value := str(b, "offset"); value != "" {
			var err error
			offset, err = strconv.ParseInt(value, 10, 64)
			if err != nil {
				return nil, apierror.Invalid("invalid_cli_cursor")
			}
		}
		return s.Jobs.Get(id, str(b, "id"), offset)
	case "/api/native/files":
		items, err := s.Files.List(id)
		return map[string]any{"items": items}, err
	case "/api/native/file":
		data, name, err := s.Files.Read(id, str(b, "id"))
		return download{data, name, "application/octet-stream"}, err
	}
	return nil, apierror.New(404, "not_found")
}
func (s *Server) nativePost(ctx context.Context, id auth.Identity, path, key string, b map[string]any) (any, error) {
	switch path {
	case "/api/native/execute":
		req, err := cli.DecodeRequest(b)
		if err != nil {
			return nil, err
		}
		result, err := s.Jobs.Submit(ctx, id, req, key)
		if err != nil {
			return nil, err
		}
		return struct {
			OK bool `json:"ok"`
			cli.Result
		}{true, result}, nil
	case "/api/native/cancel", "/api/native/input":
		value := ""
		if raw, ok := b["input"]; ok {
			var valid bool
			value, valid = raw.(string)
			if !valid {
				return nil, apierror.Invalid("invalid_cli_input")
			}
		}
		err := s.Jobs.Control(id, str(b, "id"), strings.TrimPrefix(path, "/api/native/"), value)
		return map[string]any{"ok": true, "id": str(b, "id")}, err
	case "/api/native/file/create":
		output := false
		if value, exists := b["output"]; exists {
			var ok bool
			output, ok = value.(bool)
			if !ok {
				return nil, apierror.Invalid("invalid_cli_file")
			}
		}
		file, err := s.Files.Create(id, str(b, "name"), output)
		return struct {
			OK bool `json:"ok"`
			cli.File
		}{true, file}, err
	case "/api/native/file/append":
		offset, err := integer(b, "offset")
		if err != nil {
			return nil, apierror.Invalid("invalid_cli_upload")
		}
		value, ok := b["data"].(string)
		if !ok {
			return nil, apierror.Invalid("invalid_cli_upload")
		}
		next, err := s.Files.Append(id, str(b, "id"), offset, value)
		return map[string]any{"ok": true, "offset": next}, err
	case "/api/native/file/delete":
		return map[string]bool{"ok": true}, s.Files.Delete(id, str(b, "id"))
	}
	return nil, apierror.New(404, "not_found")
}

type download struct {
	data       []byte
	name, mime string
}

func number(value any) (int64, error) {
	switch n := value.(type) {
	case json.Number:
		return n.Int64()
	case int:
		return int64(n), nil
	case int64:
		return n, nil
	case string:
		return strconv.ParseInt(n, 10, 64)
	}
	return 0, apierror.Invalid("invalid_request")
}
