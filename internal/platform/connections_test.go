package platform

import (
	"context"
	"os"
	"testing"
)

func TestConnectionVisibilityAndLoopbackFilter(t *testing.T) {
	if _, err := Connections(context.Background(), os.Getpid()); err != nil {
		t.Fatal(err)
	}
	for _, endpoint := range []string{"127.0.0.1:1234", "[::1]:1234", "[::ffff:127.0.0.1]:1234", "bad"} {
		if _, ok := remoteConnection(endpoint, "ESTABLISHED"); ok {
			t.Fatal("local/invalid connection admitted", endpoint)
		}
	}
	if row, ok := remoteConnection("203.0.113.1:443", "ESTABLISHED"); !ok || row.IP != "203.0.113.1" {
		t.Fatal("remote connection lost")
	}
}
