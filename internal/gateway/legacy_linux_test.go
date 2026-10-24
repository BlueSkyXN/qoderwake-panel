//go:build linux

package gateway

import (
	"context"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"testing"
	"time"
)

func TestLegacyLinuxGatewayKeepsOriginalTransactions(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("legacy Python runtime unavailable")
	}
	current, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	root, _ = filepath.EvalSymlinks(root)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	legacy := Legacy{Root: root, ConfigPath: filepath.Join(root, "config", "uplink-gw.json"), Port: port, Upstream: "openapi.qoder.sh", User: current.Username}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Second)
	defer cancel()
	t.Cleanup(func() {
		bounded, done := context.WithTimeout(context.Background(), 30*time.Second)
		defer done()
		if IsLegacy(root) {
			if _, err := legacy.Call(bounded, "stop", nil); err != nil {
				t.Error("legacy cleanup", err)
			}
		}
	})
	strict := strictPolicy()
	if _, err = legacy.Call(ctx, "apply", &strict); err != nil {
		t.Fatal("legacy first apply", err)
	}
	if !IsLegacy(root) {
		t.Fatal("legacy state missing")
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	control, err := NewControl(root, binary, "", "", current.Username, port)
	if err != nil {
		t.Fatal(err)
	}
	state := control.Status(ctx)
	if state["healthy"] != true || state["driver"] != "legacy" || control.Native.Upstream != "openapi.qoder.sh" {
		t.Fatal("legacy status not preserved", state)
	}
	if _, err = control.Apply(ctx, sinkPolicy()); err != nil {
		t.Fatal("legacy update", err)
	}
	if _, err = control.Rollback(ctx); err != nil {
		t.Fatal("legacy rollback", err)
	}
	state = control.Status(ctx)
	if state["healthy"] != true || state["mode"] != "strict" || !IsLegacy(root) {
		t.Fatal("legacy transaction changed runtime contract", state)
	}
	if err = control.Stop(ctx); err != nil {
		t.Fatal(err)
	}
}
