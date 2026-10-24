package server

import (
	"context"
	"qoderwake-panel/internal/managed"
	"qoderwake-panel/internal/platform"
)

func (s *Server) egress(ctx context.Context) ([]map[string]any, bool) {
	result := []map[string]any{}
	_, state, err := s.daemonController(ctx)
	if err != nil || state == nil {
		return result, false
	}
	rows, err := platform.Connections(ctx, state.PID)
	if err != nil || managed.IdentityStatus(ctx, state) != "match" {
		return result, false
	}
	whitelist := map[string]string{}
	for _, row := range s.whitelist() {
		whitelist[str(row, "ip")] = str(row, "label")
	}
	for _, row := range rows {
		label, ok := whitelist[row.IP]
		if !ok {
			label = "未列入静态清单"
		}
		result = append(result, map[string]any{"dest": row.Destination, "label": label, "ok": ok})
	}
	return result, true
}
