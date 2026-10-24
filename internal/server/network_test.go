package server

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestNetworkPolicyPreflightApplyAndReadback(t *testing.T) {
	s, _ := testServer(t)
	s.Daemon = idleDaemon{}
	if runtime.GOOS == "linux" && os.Geteuid() == 0 {
		u, err := user.Current()
		if err != nil {
			t.Fatal(err)
		}
		s.Config.GatewayUser = u.Username
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s.Config.GatewayPort = listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	c, err := s.gatewayControl()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := c.Stop(ctx); err != nil {
			t.Error(err)
		}
	})
	raw, csrf := login(t, s, "owner")
	get := func() map[string]any {
		t.Helper()
		w := request(t, s, "GET", "/api/net/state", raw, csrf, nil)
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		var value map[string]any
		if json.Unmarshal(w.Body.Bytes(), &value) != nil {
			t.Fatal("invalid state")
		}
		return value
	}
	state := get()
	if state["gw_alive"] != false || state["configRevision"] != "missing" {
		t.Fatal("fresh gateway state", state)
	}
	body := map[string]any{"mode": "enforce", "sink_post_prefixes": []string{"/algo/api/v1/tracking"}, "baseRevision": "missing"}
	if w := request(t, s, "POST", "/api/net/config", raw, csrf, body); w.Code != 409 {
		t.Fatal("confirmation omitted")
	}
	body["confirm"] = "/api/net/config"
	body["mode"] = "strict"
	if w := request(t, s, "POST", "/api/net/config", raw, csrf, body); w.Code != 400 {
		t.Fatal("strict enabled through unvalidated UI")
	}
	body["mode"] = "enforce"
	body["sink_post_prefixes"] = []string{"/unvalidated"}
	if w := request(t, s, "POST", "/api/net/config", raw, csrf, body); w.Code != 400 {
		t.Fatal("unvalidated path accepted")
	}
	body["sink_post_prefixes"] = []string{"/algo/api/v1/tracking"}
	if w := request(t, s, "POST", "/api/net/config", raw, csrf, body); w.Code != 200 {
		t.Fatal("network apply", w.Code, w.Body.String())
	}
	state = get()
	active := state["active"].(map[string]any)
	if state["gw_alive"] != true || active["matchesSaved"] != true || s.Admission.Status().Mode != "normal" {
		t.Fatal("runtime/save/admission mismatch", state)
	}
	if w := request(t, s, "POST", "/api/net/config", raw, csrf, body); w.Code != 409 {
		t.Fatal("stale config accepted")
	}
	body["baseRevision"] = state["configRevision"]
	body["mode"] = "observe"
	if w := request(t, s, "POST", "/api/net/config", raw, csrf, body); w.Code != 200 {
		t.Fatal("second apply", w.Code, w.Body.String())
	}
	path := filepath.Join(c.Native.Root, "logs", "gateway", "uplink-gw.jsonl")
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString("{\"path\":\"/catalog\",\"method\":\"GET\",\"status\":200}\n{\"path\":\"/algo/api/v1/tracking\",\"method\":\"POST\",\"q\":12,\"status\":204}\n")
	f.Close()
	state = get()
	if state["total"] != float64(2) || state["up_bytes"] != float64(12) || state["active"].(map[string]any)["rollbackAvailable"] != true {
		t.Fatal("network log window drift", state)
	}
	if _, err := s.Auth.CreateUser("reader", "Reader", "observer", nil, "example-password-for-tests", false); err != nil {
		t.Fatal(err)
	}
	reader, readerCSRF := login(t, s, "reader")
	if w := request(t, s, "GET", "/api/net/state", reader, readerCSRF, nil); w.Code != 403 {
		t.Fatal("network config exposed to observer")
	}
}
