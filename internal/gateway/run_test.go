package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestNativeGatewayRunPreflightAndShutdown(t *testing.T) {
	root := t.TempDir()
	root, _ = filepath.EvalSymlinks(root)
	path := filepath.Join(root, "config.json")
	if err := os.WriteFile(path, []byte(`{"mode":"strict"}`), 0600); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	cfg := RunConfig{Root: root, ConfigPath: path, Port: port, Upstream: "openapi.qoder.sh", Nonce: strings.Repeat("0", 32), Check: true}
	var checked bytes.Buffer
	if err := Run(context.Background(), cfg, &checked); err != nil {
		t.Fatal(err)
	}
	var receipt map[string]any
	if json.Unmarshal(checked.Bytes(), &receipt) != nil || receipt["ok"] != true {
		t.Fatal("invalid preflight output")
	}
	cfg.ExpectedConfigHash = receipt["configHash"].(string)
	cfg.ExpectedBinaryHash = receipt["binaryHash"].(string)
	cfg.Check = false
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- Run(ctx, cfg, io.Discard) }()
	client := &http.Client{Timeout: time.Second, Transport: &http.Transport{Proxy: nil}}
	defer client.CloseIdleConnections()
	origin := "http://127.0.0.1:" + strconv.Itoa(port)
	deadline := time.Now().Add(10 * time.Second)
	var health map[string]any
	for {
		response, e := client.Get(origin + HealthPath)
		if e == nil {
			e = json.NewDecoder(response.Body).Decode(&health)
			response.Body.Close()
			if e != nil {
				t.Fatal(e)
			}
			break
		}
		select {
		case err := <-done:
			t.Fatal("gateway exited before readiness", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("gateway not ready")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if health["binaryHash"] != receipt["binaryHash"] || health["configHash"] != receipt["configHash"] || health["nonce"] != cfg.Nonce || health["schemaVersion"] != float64(ProtocolVersion) {
		t.Fatal("health identity mismatch")
	}
	response, err := client.Post(origin+"/synthetic-private?token=secret", "application/json", strings.NewReader(`{"secret":"body"}`))
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 403 {
		t.Fatal("strict request forwarded")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(12 * time.Second):
		t.Fatal("shutdown timed out")
	}
	raw, err := os.ReadFile(NewLogger(root).Path)
	if err != nil || strings.Contains(string(raw), "secret") || strings.Contains(string(raw), "synthetic-private") {
		t.Fatal("audit missing or unsafe", err)
	}
	cfg.Check = true
	cfg.ExpectedBinaryHash = strings.Repeat("0", 64)
	if err := Run(context.Background(), cfg, io.Discard); err == nil || err.Error() != "gateway_binary_hash_mismatch" {
		t.Fatal("bad executable accepted", err)
	}
}
