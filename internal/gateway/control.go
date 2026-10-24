package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
)

type Control struct {
	Native *Manager
	Legacy Legacy
}

func NewControl(root, binary, config, upstream, account string, port int) (*Control, error) {
	uid, gid := uint32(os.Geteuid()), uint32(os.Getegid())
	if account == "" && runtime.GOOS == "linux" && os.Geteuid() == 0 {
		account = "qwgw"
	}
	if account != "" {
		record, err := user.Lookup(account)
		if err != nil {
			return nil, errors.New("gateway_user_unavailable")
		}
		u, err := strconv.ParseUint(record.Uid, 10, 32)
		if err != nil {
			return nil, errors.New("gateway_user_unavailable")
		}
		g, err := strconv.ParseUint(record.Gid, 10, 32)
		if err != nil {
			return nil, errors.New("gateway_user_unavailable")
		}
		uid, gid = uint32(u), uint32(g)
	} else if current, err := user.Current(); err == nil {
		account = current.Username
	}
	inherit := upstream == ""
	if inherit {
		upstream = "openapi.qoder.sh"
	}
	m, err := NewManager(root, binary, config, upstream, port, uid, gid)
	if err != nil {
		return nil, err
	}
	if inherit {
		raw, err := regular(filepath.Join(m.store(), "current.json"), 1024*1024)
		if err == nil {
			var record struct {
				Version    int `json:"schemaVersion"`
				Generation struct {
					Upstream string `json:"upstream"`
				} `json:"generation"`
			}
			if json.Unmarshal(raw, &record) == nil && (record.Version == 1 || record.Version == ProtocolVersion) && ApprovedUpstream(record.Generation.Upstream) {
				m.Upstream = record.Generation.Upstream
			}
		}
	}
	legacy := Legacy{Root: m.Root, ConfigPath: m.ConfigPath, Upstream: m.Upstream, User: account, Port: port}
	return &Control{m, legacy}, nil
}
func (c *Control) Status(ctx context.Context) map[string]any {
	if !IsLegacy(c.Native.Root) {
		result := c.Native.Status(ctx)
		result["driver"] = "native"
		return result
	}
	result, err := c.Legacy.Call(ctx, "status", nil)
	if err != nil {
		return map[string]any{"managed": true, "healthy": false, "driver": "legacy", "error": err.Error()}
	}
	result["mode"] = result["activeMode"]
	result["configHash"] = result["activeConfigHash"]
	result["driver"] = "legacy"
	result["rollbackAvailable"] = false
	raw, err := regular(filepath.Join(c.Native.store(), "current.json"), 1024*1024)
	if err == nil {
		var state struct {
			Previous json.RawMessage `json:"previous"`
		}
		if json.Unmarshal(raw, &state) == nil && len(state.Previous) > 0 && string(state.Previous) != "null" {
			result["rollbackAvailable"] = true
		}
	}
	return result
}
func (c *Control) Preflight(ctx context.Context, p Policy) (map[string]any, error) {
	if IsLegacy(c.Native.Root) {
		return c.Legacy.Call(ctx, "preflight", &p)
	}
	return c.Native.Preflight(ctx, p)
}
func (c *Control) Apply(ctx context.Context, p Policy) (map[string]any, error) {
	if IsLegacy(c.Native.Root) {
		return c.Legacy.Call(ctx, "apply", &p)
	}
	return c.Native.Apply(ctx, p)
}
func (c *Control) Rollback(ctx context.Context) (map[string]any, error) {
	if IsLegacy(c.Native.Root) {
		return c.Legacy.Call(ctx, "rollback", nil)
	}
	return c.Native.Rollback(ctx)
}
func (c *Control) Stop(ctx context.Context) error {
	if IsLegacy(c.Native.Root) {
		_, err := c.Legacy.Call(ctx, "stop", nil)
		return err
	}
	return c.Native.Stop(ctx)
}
