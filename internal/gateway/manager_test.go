package gateway

import (
	"context"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"qoderwake-panel/internal/platform"
	"strconv"
	"syscall"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	if len(os.Args) >= 3 && os.Args[1] == "gateway" && os.Args[2] == "serve" {
		port, err := strconv.Atoi(os.Getenv("QW_GW_PORT"))
		if err != nil {
			os.Exit(2)
		}
		cfg := RunConfig{Root: os.Getenv("QW_ROOT"), ConfigPath: os.Getenv("QW_GW_CONFIG"), Port: port, Upstream: os.Getenv("QW_GW_UPSTREAM"), Nonce: os.Getenv("QW_GW_NONCE"), ExpectedConfigHash: os.Getenv("QW_GW_CONFIG_HASH"), ExpectedBinaryHash: os.Getenv("QW_GW_BINARY_HASH"), Check: len(os.Args) == 4 && os.Args[3] == "--check"}
		ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
		defer cancel()
		if err = Run(ctx, cfg, os.Stdout); err != nil {
			os.Exit(2)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}
func managerFixture(t *testing.T, port int) *Manager {
	t.Helper()
	root := filepath.Join(t.TempDir(), "gateway space & 文档")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if port == 0 {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		port = listener.Addr().(*net.TCPAddr).Port
		listener.Close()
	}
	m, err := NewManager(root, binary, "", "openapi.qoder.sh", port, uint32(os.Geteuid()), uint32(os.Getegid()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		if err := m.Stop(ctx); err != nil {
			t.Error("fixture cleanup", err)
		}
	})
	return m
}
func strictPolicy() Policy { p := DefaultPolicy(); p.Mode = "strict"; return p }
func sinkPolicy() Policy {
	p := DefaultPolicy()
	p.Mode = "enforce"
	p.Sink = []string{"/tracking"}
	return p
}
func TestGatewayManagedApplyRollbackAndIdentity(t *testing.T) {
	m := managerFixture(t, 0)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Second)
	defer cancel()
	if _, err := m.Preflight(ctx, strictPolicy()); err != nil {
		t.Fatal("preflight", err)
	}
	if !m.portFree(ctx) {
		t.Fatal("preflight started a listener")
	}
	if _, err := os.Stat(m.ConfigPath); !os.IsNotExist(err) {
		t.Fatal("preflight changed saved config")
	}
	if _, err := m.Apply(ctx, strictPolicy()); err != nil {
		t.Fatal("first apply", err)
	}
	first, err := m.readState()
	if err != nil || !m.healthy(ctx, first) {
		t.Fatal("first state", err)
	}
	if _, err := m.Apply(ctx, sinkPolicy()); err != nil {
		t.Fatal("second apply", err)
	}
	second, _ := m.readState()
	if second.Previous == nil || second.Previous.Path != first.Generation.Path || m.identity(ctx, first) != "exited" {
		t.Fatal("previous generation not retained")
	}
	altered := *second
	altered.Process.PID++
	if err := m.writeJSON("current.json", &altered); err != nil {
		t.Fatal(err)
	}
	if err := m.Stop(ctx); err == nil {
		t.Fatal("mismatched PID accepted")
	}
	if !m.healthy(ctx, second) {
		t.Fatal("foreign PID check affected running gateway")
	}
	if err := m.writeJSON("current.json", second); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Rollback(ctx); err != nil {
		t.Fatal("rollback", err)
	}
	restored, _ := m.readState()
	saved, err := ReadPolicy(m.ConfigPath)
	if err != nil || saved.Hash() != strictPolicy().Hash() || restored.Generation.Path != first.Generation.Path || restored.Process.PID == first.Process.PID {
		t.Fatal("rollback state/config mismatch", err)
	}
	if err := m.Stop(ctx); err != nil {
		t.Fatal("stop", err)
	}
}
func TestGatewayCrashRecoveryBeforePublish(t *testing.T) {
	m := managerFixture(t, 0)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Second)
	defer cancel()
	if _, err := m.Apply(ctx, strictPolicy()); err != nil {
		t.Fatal(err)
	}
	previous, _ := m.readState()
	g, err := m.snapshot(sinkPolicy())
	if err != nil {
		t.Fatal(err)
	}
	j := &Journal{SchemaVersion: ProtocolVersion, OperationID: token(16), Action: "apply", Phase: "prepared", Candidate: g, Previous: previous, Nonce: token(16)}
	if err = m.writeJSON("operation.json", j); err != nil {
		t.Fatal(err)
	}
	if err = m.stop(ctx, previous); err != nil {
		t.Fatal(err)
	}
	j.Phase = "starting"
	if err = m.writeJSON("operation.json", j); err != nil {
		t.Fatal(err)
	}
	started, err := m.start(ctx, g, j.Nonce, &previous.Generation)
	if err != nil {
		t.Fatal(err)
	}
	// 子进程已运行、started 身份尚未写入日志时中断；恢复必须重新发现并核对它。
	if err = m.Recover(ctx); err != nil {
		t.Fatal("recovery", err)
	}
	restored, _ := m.readState()
	observed, observationError := platform.Observe(ctx, started.Process.PID)
	if !m.healthy(ctx, restored) || restored.Generation != previous.Generation || observationError == nil && platform.SameBirth(observed, started.Process) {
		t.Fatal("recovery did not restore previous generation")
	}
	if j, err := m.readJournal(); err != nil || j != nil {
		t.Fatal("journal remained", err)
	}
}
func TestGatewayInterruptedCommitReconcilesWithoutRestart(t *testing.T) {
	m := managerFixture(t, 0)
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Second)
	defer cancel()
	if _, err := m.Apply(ctx, strictPolicy()); err != nil {
		t.Fatal(err)
	}
	state, _ := m.readState()
	j := &Journal{SchemaVersion: ProtocolVersion, OperationID: token(16), Action: "apply", Phase: "committing", Candidate: state.Generation, Started: state, Nonce: state.Nonce}
	if err := m.writeJSON("operation.json", j); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(m.ConfigPath, []byte(`{"mode":"observe"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := m.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	current, _ := m.readState()
	p, err := ReadPolicy(m.ConfigPath)
	if err != nil || p.Hash() != state.Generation.ConfigHash || current.Process.PID != state.Process.PID {
		t.Fatal("commit recovery restarted or mismatched config", err)
	}
}
func TestGatewayForeignPortIsNotTakenOver(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	m := managerFixture(t, listener.Addr().(*net.TCPAddr).Port)
	t.Cleanup(func() { listener.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err = m.Apply(ctx, strictPolicy()); err == nil {
		t.Fatal("foreign listener taken over")
	}
	connection, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal("foreign listener changed")
	}
	connection.Close()
	if err = m.Stop(ctx); err == nil {
		t.Fatal("foreign listener stopped")
	}
}
