package server

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"qoderwake-panel/internal/gateway"
	"qoderwake-panel/internal/managed"
)

// 子进程只提供合成健康端点，生命周期测试不会访问或启动官方 daemon。
func TestMain(m *testing.M) {
	if len(os.Args) >= 3 && os.Args[1] == "gateway" && os.Args[2] == "serve" {
		port, err := strconv.Atoi(os.Getenv("QW_GW_PORT"))
		if err != nil {
			os.Exit(2)
		}
		cfg := gateway.RunConfig{Root: os.Getenv("QW_ROOT"), ConfigPath: os.Getenv("QW_GW_CONFIG"), Port: port, Upstream: os.Getenv("QW_GW_UPSTREAM"), Nonce: os.Getenv("QW_GW_NONCE"), ExpectedConfigHash: os.Getenv("QW_GW_CONFIG_HASH"), ExpectedBinaryHash: os.Getenv("QW_GW_BINARY_HASH"), Check: len(os.Args) == 4 && os.Args[3] == "--check"}
		ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
		defer cancel()
		if err = gateway.Run(ctx, cfg, os.Stdout); err != nil {
			os.Exit(2)
		}
		os.Exit(0)
	}
	if len(os.Args) >= 3 && os.Args[1] == "start" && os.Args[2] == "--foreground" {
		raw, err := os.ReadFile(filepath.Join(os.Getenv("QODERWAKE_HOME"), "fixture-port"))
		if err != nil {
			os.Exit(2)
		}
		listener, err := net.Listen("tcp", "127.0.0.1:"+strings.TrimSpace(string(raw)))
		if err != nil {
			os.Exit(2)
		}
		handler := http.NewServeMux()
		handler.HandleFunc("/api/health", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"version":"fixture-1.1.13"}`)) })
		server := &http.Server{Handler: handler, ReadHeaderTimeout: 2 * time.Second}
		done := make(chan os.Signal, 1)
		signal.Notify(done, syscall.SIGTERM, syscall.SIGINT)
		go func() { <-done; server.Close() }()
		server.Serve(listener)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

type idleDaemon struct{}

func (idleDaemon) JSON(ctx context.Context, method, path string, b any) (map[string]any, error) {
	return map[string]any{"data": map[string]any{"activity": map[string]any{"hasRunningWork": false, "runningSessions": 0, "runningConversationSessions": 0, "runningConversationTasks": 0, "runningTriggerSessions": 0, "runningTriggerTasks": 0, "queuedTriggerTasks": 0}}}, nil
}
func runtimeFixture(t *testing.T) (*Server, *managed.Controller) {
	t.Helper()
	s, _ := testServer(t)
	if err := os.MkdirAll(s.Config.Home, 0700); err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	if err := os.WriteFile(filepath.Join(s.Config.Home, "fixture-port"), []byte(strconv.Itoa(port)), 0600); err != nil {
		t.Fatal(err)
	}
	s.Config.CLI, err = os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	s.Config.DaemonURL = "http://127.0.0.1:" + strconv.Itoa(port)
	s.Daemon = idleDaemon{}
	env, _ := (managed.Policy{"hotDeploy": false, "embeddingDisabled": true}).Environment(nil)
	c, err := managed.New(managed.Config{Root: s.Config.Root, Home: s.Config.Home, Launch: s.Config.CLI, Port: port, Mode: "direct", HealthURL: s.Config.DaemonURL + "/api/health", Environment: env})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if _, err := c.Stop(ctx); err != nil {
			t.Error(err)
		}
	})
	if _, err := c.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	return s, c
}
func TestRuntimeUnmanagedPolicyAndHTTPMethods(t *testing.T) {
	s, _ := testServer(t)
	raw, csrf := login(t, s, "owner")
	state := request(t, s, "GET", "/api/runtime", raw, csrf, nil)
	if state.Code != 200 || !strings.Contains(state.Body.String(), `"restartAllowed":false`) {
		t.Fatal("unmanaged instance marked restartable", state.Code)
	}
	body := map[string]any{"hotDeploy": true, "embeddingDisabled": false, "sessionProjectionUplink": nil, "sdkByok": false}
	if w := request(t, s, "POST", "/api/runtime/policy", raw, csrf, body); w.Code != 409 {
		t.Fatal("confirmation omitted", w.Code)
	}
	body["confirm"] = "/api/runtime/policy"
	if w := request(t, s, "POST", "/api/runtime/policy", raw, csrf, body); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	policy, err := managed.ReadPolicy(s.Config.Root)
	if err != nil || policy["hotDeploy"] != true || policy["sdkByok"] != false {
		t.Fatal("policy not saved", err)
	}
	body["sdkByok"] = "false"
	if w := request(t, s, "POST", "/api/runtime/policy", raw, csrf, body); w.Code != 400 {
		t.Fatal("invalid type accepted", w.Code)
	}
	for _, path := range []string{"/api/runtime/apply", "/api/apply"} {
		if w := request(t, s, "GET", path, raw, csrf, nil); w.Code == 200 {
			t.Fatal("GET accepted mutation")
		}
		if w := request(t, s, "POST", path, raw, csrf, map[string]any{"confirm": path}); w.Code != 409 {
			t.Fatal("unmanaged restart accepted", w.Code)
		}
	}
}
func TestRuntimeApplyDrainsRefreshesIdentityAndUsesLatestPolicy(t *testing.T) {
	s, c := runtimeFixture(t)
	raw, csrf := login(t, s, "owner")
	before, _ := c.Read()
	lease, _ := s.Admission.Acquire(false)
	defer lease.Release()
	finished := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		finished <- request(t, s, "POST", "/api/runtime/apply", raw, csrf, map[string]any{"confirm": "/api/runtime/apply"})
	}()
	waitMode := func() {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for s.Admission.Status().Mode != "draining" {
			if time.Now().After(deadline) {
				t.Fatal("apply did not enter drain")
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	waitMode()
	if current, _ := c.Read(); current.PID != before.PID {
		t.Fatal("restarted before drain")
	}
	// 模拟在排空前已进入的策略保存，重启应读取保存后的值。
	if err := managed.SavePolicy(s.Config.Root, managed.Policy{"hotDeploy": true, "embeddingDisabled": false}); err != nil {
		t.Fatal(err)
	}
	lease.Release()
	w := <-finished
	if w.Code != 200 {
		t.Fatal("apply failed", w.Code, w.Body.String())
	}
	after, _ := c.Read()
	if after.PID == before.PID || after.Environment["QODERWAKE_HOT_DEPLOY"] != "1" {
		t.Fatal("restart did not apply latest policy")
	}
	if s.Admission.Status().Mode != "normal" {
		t.Fatal("admission stuck")
	}
	user, err := s.Auth.CreateUser("second", "Second", "admin", nil, "example-password-for-tests", false)
	if err != nil {
		t.Fatal(err)
	}
	raw, csrf = login(t, s, "second")
	lease, _ = s.Admission.Acquire(false)
	go func() {
		finished <- request(t, s, "POST", "/api/runtime/apply", raw, csrf, map[string]any{"confirm": "/api/runtime/apply"})
	}()
	waitMode()
	if _, err := s.Auth.UpdateUser(user.ID, "Second", "observer", nil, true, user.Revision); err != nil {
		t.Fatal(err)
	}
	lease.Release()
	w = <-finished
	if w.Code != 401 {
		t.Fatal("revoked identity accepted", w.Code, w.Body.String())
	}
	current, _ := c.Read()
	if current.PID != after.PID {
		t.Fatal("revoked user restarted daemon")
	}
	state, err := s.runtimeState(context.Background())
	if err != nil || state["restartAllowed"] != true || state["pendingRestart"] != false {
		b, _ := json.Marshal(state)
		t.Fatal("observed runtime invalid", string(b), err)
	}
}
