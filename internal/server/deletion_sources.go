package server

import (
	"context"
	"encoding/json"
	"net/url"
	"strconv"
	"strings"

	"qoderwake-panel/internal/apierror"
	"qoderwake-panel/internal/daemon"
	"qoderwake-panel/internal/deletion"
	"qoderwake-panel/internal/provider"
)

func opaqueID(value string) (string, error) {
	if len([]rune(value)) < 1 || len([]rune(value)) > 256 || value == "." || value == ".." || strings.ContainsAny(value, "/\\?#%") {
		return "", apierror.Invalid("invalid_identifier")
	}
	for _, r := range value {
		if r < 32 || r == 127 {
			return "", apierror.Invalid("invalid_identifier")
		}
	}
	return value, nil
}
func strictRows(value any, max int) ([]any, error) {
	rows, ok := value.([]any)
	if !ok || rows == nil || len(rows) > max {
		return nil, apierror.Invalid("deletion_source_invalid")
	}
	for _, v := range rows {
		if row, ok := v.(map[string]any); !ok || row == nil {
			return nil, apierror.Invalid("deletion_source_invalid")
		}
	}
	return rows, nil
}
func sourceData(raw map[string]any) any {
	if value, ok := raw["data"]; ok {
		return value
	}
	return raw
}
func jsonValue(value any) any {
	raw, _ := json.Marshal(value)
	var out any
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	decoder.Decode(&out)
	return out
}
func (s *Server) deletionSource(ctx context.Context) deletion.Source {
	return func(name, target string) (any, error) {
		switch name {
		case "waker_detail":
			return s.waker(ctx, target)
		case "provider_models":
			snapshot, e := s.Providers.Read()
			if e != nil {
				return nil, e
			}
			value := snapshot.Data["providers"].(map[string]any)[target]
			if editable, _ := provider.Editability(value); !editable {
				return nil, apierror.Invalid("provider_requires_official_editor")
			}
			return jsonValue(provider.Models(target, value)), nil
		case "callers":
			value, e := s.Auth.Callers()
			return jsonValue(value), e
		case "activity":
			value, e := s.systemStatus(ctx)
			if e != nil {
				return nil, e
			}
			return value["activity"], nil
		case "preference":
			if _, e := safeID(target); e != nil {
				return nil, e
			}
			raw, e := s.Daemon.JSON(ctx, "GET", "/api/model-preferences/"+target, nil)
			if e != nil {
				return nil, e
			}
			value := daemon.Data(raw)
			_, one := value["noProject"]
			many, hasMany := value["byProject"]
			if !one && !hasMany {
				return nil, apierror.Invalid("model_preference_unavailable")
			}
			if hasMany {
				if object, ok := many.(map[string]any); !ok || object == nil {
					return nil, apierror.Invalid("model_preference_unavailable")
				}
			}
			return value, nil
		case "wakers":
			raw, e := s.Daemon.JSON(ctx, "GET", "/api/agents", nil)
			if e != nil {
				return nil, e
			}
			value := raw["data"]
			if object, ok := value.(map[string]any); ok {
				value = object["list"]
				if value == nil {
					value = object["items"]
				}
			}
			items, e := strictRows(value, 10000)
			if e != nil {
				return nil, e
			}
			out := []any{}
			for _, item := range items {
				row := item.(map[string]any)
				wid := str(row, "agentId")
				if wid == "" {
					wid = str(row, "id")
				}
				if _, e = safeID(wid); e != nil {
					return nil, e
				}
				out = append(out, map[string]any{"agentId": wid, "name": row["name"]})
			}
			return out, nil
		case "channels":
			raw, e := s.Daemon.JSON(ctx, "GET", "/api/channels", nil)
			if e != nil {
				return nil, e
			}
			value := raw["data"]
			if object, ok := value.(map[string]any); ok {
				value = object["items"]
				if value == nil {
					value = object["channels"]
				}
			}
			return strictRows(value, 10000)
		case "groups":
			raw, e := s.Daemon.JSON(ctx, "GET", "/api/conversation-groups", nil)
			if e != nil {
				return nil, e
			}
			object, ok := sourceData(raw).(map[string]any)
			if !ok {
				return nil, apierror.Invalid("invalid_group_list")
			}
			groups, e := strictRows(object["groups"], 10000)
			if e != nil {
				return nil, e
			}
			out := []any{}
			for _, value := range groups {
				row := value.(map[string]any)
				gid := str(row, "id")
				if gid == "" {
					gid = str(row, "groupId")
				}
				if gid == "" {
					gid = str(row, "group_id")
				}
				if _, e = opaqueID(gid); e != nil {
					return nil, e
				}
				raw, e := s.Daemon.JSON(ctx, "GET", "/api/conversation-surface-groups/"+url.PathEscape(gid), nil)
				if e != nil {
					return nil, e
				}
				detail, ok := sourceData(raw).(map[string]any)
				if !ok || detail == nil {
					return nil, apierror.Invalid("invalid_group_detail")
				}
				group := detail
				if value, exists := detail["group"]; exists {
					group, ok = value.(map[string]any)
					if !ok || group == nil {
						return nil, apierror.Invalid("invalid_group_detail")
					}
				}
				found := false
				for _, key := range []string{"id", "groupId", "group_id"} {
					if value, exists := group[key]; exists {
						found = true
						if value != gid {
							return nil, apierror.Invalid("invalid_group_detail")
						}
					}
				}
				if !found {
					return nil, apierror.Invalid("invalid_group_detail")
				}
				out = append(out, detail)
			}
			return out, nil
		case "workflows":
			path := "/api/workflows"
			if target != "" {
				if _, e := safeID(target); e != nil {
					return nil, e
				}
				path = "/api/agents/" + target + "/workflows"
			}
			raw, e := s.Daemon.JSON(ctx, "GET", path, nil)
			if e != nil {
				return nil, e
			}
			value := sourceData(raw)
			if object, ok := value.(map[string]any); ok {
				value = object["workflows"]
			}
			return strictRows(value, 10000)
		case "triggers", "pairings", "sessions":
			return s.deletionPages(ctx, name, target)
		}
		return nil, apierror.Invalid("deletion_source_invalid")
	}
}
func (s *Server) deletionPages(ctx context.Context, kind, target string) ([]any, error) {
	out := []any{}
	if kind == "sessions" {
		if _, e := safeID(target); e != nil {
			return nil, e
		}
	}
	maxPages := 100
	if kind == "triggers" {
		maxPages = 500
	}
	for page := 0; page < maxPages; page++ {
		offset := page * 100
		path := ""
		switch kind {
		case "triggers":
			path = "/api/triggers?page=" + strconv.Itoa(offset/100+1) + "&pageSize=100"
		case "pairings":
			path = "/api/channels/pairing/pending?limit=100&offset=" + strconv.Itoa(offset) + "&includeTotal=true"
		case "sessions":
			path = "/api/agents/" + target + "/console-sessions/query?offset=" + strconv.Itoa(offset) + "&limit=100&pinned_first=true&task_source=all"
		}
		raw, e := s.Daemon.JSON(ctx, "GET", path, nil)
		if e != nil {
			return nil, e
		}
		if kind == "triggers" {
			if values, ok := raw["data"].([]any); ok {
				items, e := strictRows(values, 99)
				if e != nil || len(out)+len(items) > 10000 {
					return nil, apierror.Invalid("trigger_pagination_unavailable")
				}
				return append(out, items...), nil
			}
		}
		data := daemon.Data(raw)
		limit := 100
		if kind == "pairings" {
			limit = 10000
		}
		items, e := strictRows(data["items"], limit)
		if e != nil {
			return nil, e
		}
		if kind == "sessions" {
			for _, item := range items {
				row := item.(map[string]any)
				if value := row["local_agent_id"]; value != nil && value != "" && value != target {
					return nil, apierror.Invalid("invalid_session_page")
				}
			}
		}
		out = append(out, items...)
		if len(out) > 10000 {
			return nil, apierror.Invalid("deletion_scan_limit")
		}
		if kind == "sessions" {
			value, exists := data["has_more"]
			if !exists {
				value = data["hasMore"]
			}
			more, ok := value.(bool)
			if !ok {
				return nil, apierror.Invalid("invalid_session_page")
			}
			if !more {
				return out, nil
			}
		} else {
			totalValue := data["total"]
			if kind == "triggers" {
				pagination, ok := data["pagination"].(map[string]any)
				if !ok {
					return nil, apierror.Invalid("trigger_pagination_unavailable")
				}
				totalValue = pagination["total"]
			}
			total, e := number(totalValue)
			if e != nil || total < 0 || total > 10000 {
				return nil, apierror.Invalid("deletion_source_invalid")
			}
			if int64(len(out)) >= total {
				if int64(len(out)) != total {
					return nil, apierror.Invalid("deletion_source_invalid")
				}
				return out, nil
			}
		}
		if len(items) == 0 {
			return nil, apierror.Invalid("deletion_source_invalid")
		}
	}
	return nil, apierror.Invalid("deletion_scan_limit")
}
