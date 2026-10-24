//go:build linux

package platform

import "testing"

func TestProcConnectionIPv4IPv6(t *testing.T) {
	for input, want := range map[string]string{"0100007F:01BB": "127.0.0.1:443", "00000000000000000000000001000000:01BB": "[::1]:443", "010000CB:01BB": "203.0.0.1:443"} {
		got, ok := procEndpoint(input)
		if !ok || got != want {
			t.Fatal(input, got)
		}
	}
}
