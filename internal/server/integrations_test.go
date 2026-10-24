package server

import (
	"bytes"
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestChannelConfigMatchesPythonAndKeepsUnknownFields(t *testing.T) {
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("Python reference unavailable")
	}
	cases := []struct{ Body, Existing map[string]any }{
		{map[string]any{"type": "feishu", "appId": "example", "appSecret": "synthetic", "botName": "测试"}, nil},
		{map[string]any{"type": "feishu", "appId": "", "appSecret": "", "botName": "changed", "accessPolicy": "open", "bindingTarget": "w1"}, map[string]any{"type": "feishu", "appId": "example", "appSecret": "synthetic-old", "enabled": true, "extra": map[string]any{"keep": true}, "model": "local/model", "workspace": map[string]any{"path": "synthetic-workspace"}}},
		{map[string]any{"type": "feishu", "appId": "example", "appSecret": "", "accessPolicy": "paired"}, map[string]any{"appId": "example", "appSecret": "synthetic-old", "enabled": true, "bindingTarget": map[string]any{"targetId": "w1"}, "model": "auto"}},
	}
	input, _ := json.Marshal(cases)
	dir, _ := filepath.Abs("../../tests")
	command := exec.Command(py, "-c", `import json,sys
sys.path.insert(0,sys.argv[1])
from test_panel import app
print(json.dumps([app.channel_config(x['Body'],x['Existing']) for x in json.load(sys.stdin)],ensure_ascii=False))`, dir)
	command.Stdin = bytes.NewReader(input)
	output, e := command.Output()
	if e != nil {
		t.Fatal(e)
	}
	var expected []any
	if e = json.Unmarshal(output, &expected); e != nil {
		t.Fatal(e)
	}
	for i, c := range cases {
		actual, e := channelConfig(c.Body, c.Existing)
		if e != nil {
			t.Fatal(e)
		}
		if !reflect.DeepEqual(jsonValue(actual), expected[i]) {
			t.Fatal("channel configuration differs from Python", i)
		}
	}
	for _, body := range []map[string]any{{"type": "other"}, {"type": "feishu", "appId": "example"}, {"type": "feishu", "appId": "example", "appSecret": "synthetic", "accessPolicy": "open"}, {"type": "feishu", "appId": "example", "appSecret": "synthetic", "enabled": "yes"}} {
		if _, e := channelConfig(body, nil); e == nil {
			t.Fatal("invalid channel configuration accepted")
		}
	}
}

func TestHTTPChannelFilterAndControlledSave(t *testing.T) {
	s, _ := testServer(t)
	writes := 0
	var saved map[string]any
	config := map[string]any{"id": "c1", "type": "feishu", "appId": "example", "appSecret": "synthetic-private-channel-secret", "enabled": true, "accessPolicy": "paired", "extra": "keep"}
	s.Daemon = daemonFunc(func(_ context.Context, method, path string, b any) (map[string]any, error) {
		if method == "PUT" {
			writes++
			saved = b.(map[string]any)
			return map[string]any{"success": true}, nil
		}
		if path == "/api/channels" {
			return map[string]any{"data": []any{map[string]any{"channel": map[string]any{"channelId": "c1", "config": config}}}}, nil
		}
		return map[string]any{"data": map[string]any{"channelId": "c1", "config": config}}, nil
	})
	raw, csrf := login(t, s, "owner")
	for _, path := range []string{"/api/channels", "/api/channels/detail?id=c1"} {
		w := request(t, s, "GET", path, raw, csrf, nil)
		if w.Code != 200 || strings.Contains(w.Body.String(), "synthetic-private-channel-secret") {
			t.Fatal("channel secret leaked")
		}
	}
	body := map[string]any{"id": "c1", "type": "feishu", "appId": "example", "appSecret": "", "botName": "updated", "accessPolicy": "paired"}
	if request(t, s, "POST", "/api/channels/config", raw, csrf, body).Code != 409 || writes != 0 {
		t.Fatal("channel save unconfirmed")
	}
	body["confirm"] = "/api/channels/config"
	if request(t, s, "POST", "/api/channels/config", raw, csrf, body).Code != 200 || writes != 1 {
		t.Fatal("channel save failed")
	}
	if saved["appSecret"] != "***" || saved["extra"] != "keep" || saved["enabled"] != true {
		t.Fatal("channel update changed preserved values")
	}
}

func TestPluginWritesRequireEnabledCapabilityAndExactVersion(t *testing.T) {
	s, _ := testServer(t)
	writes := 0
	var method, path string
	var payload any
	s.Daemon = daemonFunc(func(_ context.Context, m, p string, b any) (map[string]any, error) {
		writes++
		method = m
		path = p
		payload = b
		return map[string]any{"success": true}, nil
	})
	raw, csrf := login(t, s, "owner")
	body := map[string]any{"pluginId": "example?tag", "wakerId": "w1", "confirm": "/api/plugins/install"}
	if request(t, s, "POST", "/api/plugins/install", raw, csrf, body).Code != 403 || writes != 0 {
		t.Fatal("plugin writes enabled by default")
	}
	s.Config.PluginWrites = true
	if request(t, s, "POST", "/api/plugins/install", raw, csrf, body).Code != 400 || writes != 0 {
		t.Fatal("plugin installed without expected version")
	}
	body["expectedVersion"] = "1.2.3"
	if request(t, s, "POST", "/api/plugins/install", raw, csrf, body).Code != 200 || writes != 1 {
		t.Fatal("plugin install failed")
	}
	if method != "POST" || path != "/api/plugin-market/example%3Ftag/installations/w1" || payload.(map[string]any)["expectedVersion"] != "1.2.3" {
		t.Fatal("plugin arguments changed")
	}
}
