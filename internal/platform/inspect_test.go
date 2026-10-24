package platform

import (
	"context"
	"net"
	"os"
	"testing"
)

func TestCurrentProcessBirthExecutableAndListener(t *testing.T) {
	observed, err := Observe(context.Background(), os.Getpid())
	if err != nil {
		t.Fatal("observe own process", err)
	}
	if observed.PID != os.Getpid() || observed.UID != uint32(os.Geteuid()) || observed.StartTicks == 0 || observed.BootID == "" || len(observed.Cmdline) == 0 {
		t.Fatal("incomplete process identity")
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	identity, err := FileIdentity(executable)
	if err != nil {
		t.Fatal(err)
	}
	if identity != observed.Executable {
		t.Fatal("running executable identity differs from file")
	}
	current, err := Observe(context.Background(), os.Getpid())
	if err != nil || !SameBirth(observed, current) {
		t.Fatal("unstable birth identity", err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if status := PortStatus(context.Background(), os.Getpid(), port); status != "owned" {
		listener.Close()
		t.Fatal("own listening socket not verified", status)
	}
	if status := PortStatus(context.Background(), os.Getpid()+1000000, port); status != "other" {
		listener.Close()
		t.Fatal("foreign PID claimed socket", status)
	}
	listener.Close()
	if status := PortStatus(context.Background(), os.Getpid(), port); status != "unbound" {
		t.Fatal("closed socket still bound", status)
	}
}
func TestEnvironmentParsingKeepsValuesInMemory(t *testing.T) {
	env := environment([]byte("QODERWAKE_HOME=/synthetic/path with spaces\x00TEST=key=value\x00BROKEN\x00BAD=\xff\x00"))
	if env["QODERWAKE_HOME"] != "/synthetic/path with spaces" || env["TEST"] != "key=value" || len(env) != 2 {
		t.Fatal("environment parse mismatch")
	}
}
