package managed

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"reflect"
	"strconv"
	"syscall"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	if len(os.Args) >= 3 && os.Args[1] == "start" && os.Args[2] == "--foreground" {
		raw, err := os.ReadFile(filepath.Join(os.Getenv("QODERWAKE_HOME"), "helper.json"))
		if err != nil {
			os.Exit(2)
		}
		var cfg struct{ Port int }
		if json.Unmarshal(raw, &cfg) != nil || cfg.Port < 1 {
			os.Exit(2)
		}
		listener, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(cfg.Port))
		if err != nil {
			os.Exit(2)
		}
		handler := http.NewServeMux()
		handler.HandleFunc("/api/health", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"data":{"version":"fixture-1.1.13"}}`))
		})
		server := &http.Server{Handler: handler, ReadHeaderTimeout: 2 * time.Second}
		done := make(chan os.Signal, 1)
		signal.Notify(done, syscall.SIGTERM, syscall.SIGINT)
		go func() {
			<-done
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			server.Shutdown(ctx)
		}()
		err = server.Serve(listener)
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			os.Exit(2)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}
func controllerFixture(t *testing.T, port int) *Controller {
	t.Helper()
	root := filepath.Join(t.TempDir(), "managed test & 文档")
	home := filepath.Join(root, "home")
	if err := os.MkdirAll(home, 0700); err != nil {
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
	raw, _ := json.Marshal(map[string]int{"port": port})
	if err := os.WriteFile(filepath.Join(home, "helper.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	controller, err := New(Config{Root: root, Name: "fixture", Launch: executable, Home: home, Port: port, Mode: "direct", HealthURL: "http://127.0.0.1:" + strconv.Itoa(port) + "/api/health", ExpectedVersion: "fixture-1.1.13", Environment: map[string]string{"QODERWAKE_HOT_DEPLOY": "0", "QODER_MEMORY_DISABLE_EMBEDDING": "1", "QODER_SDK_CUSTOM_BASE_URL_BYOK": "1", "QODERWAKE_SESSION_PROJECTION_UPLINK": "0"}})
	if err != nil {
		t.Fatal(err)
	}
	return controller
}
func TestManagedDaemonLifecycleAndStalePIDProtection(t *testing.T) {
	controller := controllerFixture(t, 0)
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Second)
	defer cancel()
	t.Log("isolated managed fixture", controller.Config.Root)
	t.Cleanup(func() {
		bounded, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if _, err := controller.Stop(bounded); err != nil {
			t.Error("fixture cleanup", err)
		}
	})
	result, err := controller.Start(ctx)
	if err != nil {
		t.Fatal("fixture start", err)
	}
	if result["ok"] != true {
		t.Fatal("fixture not ready")
	}
	state, err := controller.Read()
	if err != nil || state == nil {
		t.Fatal("state missing", err)
	}
	if !reflect.DeepEqual(state.Cmdline, daemonCommand(controller.Config.Launch, controller.Config.Port)) {
		t.Fatal("new instance did not receive explicit loopback host and port")
	}
	status, err := controller.Status(ctx)
	if err != nil || status["healthy"] != true {
		t.Fatal("running fixture not verified", err)
	}
	altered := *state
	altered.PID++
	if err = WriteState(controller.statePath, &altered); err != nil {
		t.Fatal(err)
	}
	if _, err = controller.Stop(ctx); err == nil {
		t.Fatal("stale PID was accepted")
	}
	if IdentityStatus(ctx, state) != "match" {
		t.Fatal("stale record affected the real process")
	}
	if err = WriteState(controller.statePath, state); err != nil {
		t.Fatal(err)
	}
	controller.Config.Environment["QODERWAKE_SESSION_PROJECTION_UPLINK"] = "1"
	if _, err = controller.Start(ctx); err == nil || err.Error() != "managed_uplink_disable_not_preserved" {
		t.Fatal("disabled uplink silently restored", err)
	}
	if IdentityStatus(ctx, state) != "match" {
		t.Fatal("rejected restart stopped the old process")
	}
	if _, err = controller.Stop(ctx); err != nil {
		t.Fatal("fixture stop", err)
	}
	if current, e := controller.Read(); e != nil || current != nil {
		t.Fatal("stopped state not removed", e)
	}
	if _, err = controller.Start(ctx); err != nil {
		t.Fatal("explicit stop/start could not update uplink", err)
	}
	if status, err = controller.Status(ctx); err != nil || status["healthy"] != true {
		t.Fatal("restarted fixture not healthy", err)
	}
}
func TestControllerWillNotTakeOverUnmanagedListener(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	controller := controllerFixture(t, listener.Addr().(*net.TCPAddr).Port)
	if _, err = controller.Start(context.Background()); err == nil {
		t.Fatal("unmanaged listener was taken over")
	}
	if _, err = controller.Stop(context.Background()); err == nil {
		t.Fatal("unmanaged listener was stopped")
	}
	connection, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal("unmanaged listener changed")
	}
	connection.Close()
}
