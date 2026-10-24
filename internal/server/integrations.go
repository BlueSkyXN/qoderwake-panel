package server

import (
	"context"
	"net/url"
	"strconv"
	"strings"

	"qoderwake-panel/internal/apierror"
	"qoderwake-panel/internal/auth"
)

func pluginID(value string) (string, error) {
	value, err := textValue(value, 256, false)
	if err != nil {
		return "", err
	}
	if strings.ContainsAny(value, "/\\") || value == "." || value == ".." {
		return "", apierror.Invalid("invalid_plugin_id")
	}
	for _, r := range value {
		if r < 32 {
			return "", apierror.Invalid("invalid_plugin_id")
		}
	}
	return url.PathEscape(value), nil
}
func dataOrEmpty(raw map[string]any) any {
	if raw["data"] != nil {
		return raw["data"]
	}
	return map[string]any{}
}
func (s *Server) pluginGet(ctx context.Context, path string, b map[string]any) (any, error) {
	if path == "/api/plugins" {
		featured, installed := any(map[string]any{}), any([]any{})
		if raw, err := s.Daemon.JSON(ctx, "GET", "/api/plugin-market/featured?limit=12", nil); err == nil {
			featured = dataOrEmpty(raw)
		}
		if raw, err := s.Daemon.JSON(ctx, "GET", "/api/plugin-market/installed", nil); err == nil {
			installed = dataOrEmpty(raw)
		}
		return map[string]any{"featured": featured, "installed": installed}, nil
	}
	endpoint := "/api/plugin-market"
	switch path {
	case "/api/plugins/catalog":
		page, err := queryInt(b, "page", 1, 1, 100000)
		if err != nil {
			return nil, err
		}
		keyword, err := textValue(str(b, "keyword"), 120, true)
		if err != nil {
			return nil, err
		}
		endpoint += "?" + url.Values{"keyword": {keyword}, "page": {strconv.Itoa(page)}, "pageSize": {"20"}, "language": {"zh-CN"}}.Encode()
	case "/api/plugins/detail", "/api/plugins/installations":
		id, err := pluginID(str(b, "id"))
		if err != nil {
			return nil, err
		}
		endpoint += "/" + id + "/" + strings.TrimPrefix(path, "/api/plugins/")
		if path == "/api/plugins/detail" {
			endpoint += "?language=zh-CN"
		}
	case "/api/plugins/installed":
		endpoint += "/installed"
		if wid := str(b, "waker"); wid != "" {
			if _, err := safeID(wid); err != nil {
				return nil, err
			}
			endpoint += "?wakerId=" + wid
		}
	}
	raw, err := s.Daemon.JSON(ctx, "GET", endpoint, nil)
	if err != nil {
		return nil, err
	}
	return dataOrEmpty(raw), nil
}
func (s *Server) pluginPost(ctx context.Context, id auth.Identity, path string, b map[string]any) (any, error) {
	if !s.Config.PluginWrites {
		return nil, apierror.New(403, "experimental_plugin_writes_disabled")
	}
	if str(b, "confirm") != path {
		return nil, apierror.New(409, "confirmation_required")
	}
	release, err := s.writeLock(id)
	if err != nil {
		return nil, err
	}
	defer release()
	plugin, err := pluginID(str(b, "pluginId"))
	if err != nil {
		return nil, err
	}
	wid, err := safeID(str(b, "wakerId"))
	if err != nil {
		return nil, err
	}
	endpoint := "/api/plugin-market/" + plugin + "/installations/" + wid
	var body any
	method := "DELETE"
	switch path {
	case "/api/plugins/install":
		version, err := textValue(b["expectedVersion"], 128, false)
		if err != nil {
			return nil, err
		}
		method = "POST"
		body = map[string]any{"expectedVersion": version}
	case "/api/plugins/toggle":
		enabled, ok := b["enabled"].(bool)
		if !ok {
			return nil, apierror.Invalid("invalid_enabled")
		}
		method = "PATCH"
		body = map[string]any{"enabled": enabled}
	}
	_, err = s.Daemon.JSON(ctx, method, endpoint, body)
	return map[string]any{"ok": true}, err
}

func channelConfigValue(row map[string]any) map[string]any {
	if channel, ok := row["channel"].(map[string]any); ok {
		row = channel
	}
	if cfg, ok := row["config"].(map[string]any); ok {
		return cfg
	}
	return row
}
func channelPublic(raw map[string]any) map[string]any {
	row := raw
	if channel, ok := row["channel"].(map[string]any); ok {
		row = channel
	}
	source := channelConfigValue(raw)
	result := pick(source, "id", "channelId", "type", "name", "botName", "status", "enabled", "appId", "agentId", "bindingTarget", "accessPolicy", "artifactDeliveryEnabled")
	for _, key := range []string{"id", "channelId", "status", "agentId"} {
		if result[key] == nil {
			result[key] = row[key]
		}
	}
	if target, ok := result["bindingTarget"].(map[string]any); ok {
		result["bindingTarget"] = pick(target, "targetKind", "targetId", "displayName", "enabled")
	}
	return result
}
func (s *Server) channels(ctx context.Context) (any, error) {
	raw, err := s.Daemon.JSON(ctx, "GET", "/api/channels", nil)
	if err != nil {
		return nil, err
	}
	value := raw["data"]
	if object, ok := value.(map[string]any); ok {
		value = object["items"]
		if value == nil {
			value = object["channels"]
		}
	}
	result := []map[string]any{}
	for _, row := range rows(value) {
		result = append(result, channelPublic(row))
	}
	return result, nil
}
func (s *Server) channelDetail(ctx context.Context, id string) (map[string]any, error) {
	if _, err := safeID(id); err != nil {
		return nil, err
	}
	raw, err := s.Daemon.JSON(ctx, "GET", "/api/channels/"+id, nil)
	if err != nil {
		return nil, err
	}
	row, ok := raw["data"].(map[string]any)
	if !ok {
		return nil, apierror.Invalid("invalid_channel_response")
	}
	return channelPublic(row), nil
}
func channelConfig(body, existing map[string]any) (map[string]any, error) {
	if str(body, "type") != "feishu" {
		return nil, apierror.Invalid("unsupported_channel_type")
	}
	policy := any("paired")
	if value, ok := body["accessPolicy"]; ok {
		policy = value
	}
	if policy != "paired" && policy != "open" {
		return nil, apierror.Invalid("invalid_channel_policy")
	}
	enabled := any(false)
	if value, ok := existing["enabled"]; ok {
		enabled = value
	}
	if value, ok := body["enabled"]; ok {
		enabled = value
	}
	delivery := any(false)
	if value, ok := body["artifactDeliveryEnabled"]; ok {
		delivery = value
	}
	if _, ok := enabled.(bool); !ok {
		return nil, apierror.Invalid("invalid_channel_config")
	}
	if _, ok := delivery.(bool); !ok {
		return nil, apierror.Invalid("invalid_channel_config")
	}
	read := func(key string, fallback any, limit int) (string, error) {
		value, ok := body[key]
		if !ok {
			value = fallback
		}
		return textValue(value, limit, true)
	}
	app, err := read("appId", "", 300)
	if err != nil {
		return nil, err
	}
	if app == "" {
		app = str(existing, "appId")
	}
	if app == "" {
		return nil, apierror.Invalid("channel_app_id_required")
	}
	secret, err := read("appSecret", "", 2000)
	if err != nil {
		return nil, err
	}
	if secret == "" {
		if len(existing) == 0 {
			return nil, apierror.Invalid("channel_secret_required")
		}
		secret = "***"
	}
	name := existing["botName"]
	if name == nil {
		name = ""
	}
	botName, err := read("botName", name, 100)
	if err != nil {
		return nil, err
	}
	target := str(body, "bindingTarget")
	if target == "" {
		target = str(body, "agentId")
	}
	if target == "" {
		target = str(existing, "bindingTarget")
		if value, ok := existing["bindingTarget"].(map[string]any); ok {
			target = str(value, "targetId")
		}
	}
	if policy == "open" || target != "" {
		if _, err = safeID(target); err != nil {
			return nil, err
		}
	}
	result := map[string]any{}
	for key, value := range existing {
		result[key] = value
	}
	result["type"] = "feishu"
	result["enabled"] = enabled
	result["appId"] = app
	result["appSecret"] = secret
	result["botName"] = botName
	result["accessPolicy"] = policy
	result["artifactDeliveryEnabled"] = delivery
	for key, value := range map[string]string{"workspacePath": "", "mode": "conversation", "workflowId": "", "workflowName": ""} {
		if _, exists := result[key]; !exists {
			result[key] = value
		}
	}
	result["bindingTarget"] = nil
	result["model"] = nil
	result["workspace"] = nil
	if policy == "open" {
		model := existing["model"]
		if value, ok := body["model"]; ok {
			model = value
		}
		if model == nil || model == "" {
			model = "auto"
		}
		modelName, err := textValue(model, 300, false)
		if err != nil {
			return nil, err
		}
		workspace := existing["workspace"]
		if value, ok := body["workspace"]; ok {
			workspace = value
		}
		if workspace != nil {
			if _, ok := workspace.(map[string]any); !ok {
				return nil, apierror.Invalid("invalid_channel_workspace")
			}
		}
		result["bindingTarget"] = map[string]any{"targetKind": "waker", "targetId": target, "displayName": nil, "enabled": true}
		result["model"] = modelName
		result["workspace"] = workspace
	}
	return result, nil
}
func (s *Server) channelPost(ctx context.Context, identity auth.Identity, path string, b map[string]any) (any, error) {
	if str(b, "confirm") != path {
		return nil, apierror.New(409, "confirmation_required")
	}
	if err := bodyStrings(b, "id", "type", "appId", "appSecret", "botName", "accessPolicy", "bindingTarget", "agentId", "model"); err != nil {
		return nil, err
	}
	id := str(b, "id")
	if id != "" {
		if _, err := safeID(id); err != nil {
			return nil, err
		}
	} else if path != "/api/channels/config" {
		return nil, apierror.Invalid("invalid_identifier")
	}
	if path != "/api/channels/delete" {
		release, err := s.writeLock(identity)
		if err != nil {
			return nil, err
		}
		defer release()
	}
	switch path {
	case "/api/channels/action":
		action := str(b, "action")
		if !contains([]string{"start", "stop", "restart"}, action) {
			return nil, apierror.Invalid("invalid_channel_action")
		}
		_, err := s.Daemon.JSON(ctx, "POST", "/api/channels/"+id+"/"+action, map[string]any{})
		return map[string]any{"ok": true}, err
	case "/api/channels/delete":
		_, err := s.Daemon.JSON(ctx, "DELETE", "/api/channels/"+id, nil)
		return map[string]any{"ok": true}, err
	}
	current := map[string]any{}
	if id != "" {
		raw, err := s.Daemon.JSON(ctx, "GET", "/api/channels/"+id, nil)
		if err != nil {
			return nil, err
		}
		value, ok := raw["data"].(map[string]any)
		if !ok {
			return nil, apierror.Invalid("invalid_channel_response")
		}
		current = channelConfigValue(value)
	} else {
		id = "global-" + strings.ReplaceAll(uuid(), "-", "")[:6] + "-" + str(b, "type")
	}
	config, err := channelConfig(b, current)
	if err != nil {
		return nil, err
	}
	_, err = s.Daemon.JSON(ctx, "PUT", "/api/channels/"+id+"/config", config)
	return map[string]any{"ok": true, "id": id, "message": "通道配置已保存；请单独启动以建立平台连接"}, err
}
