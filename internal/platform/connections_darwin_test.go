//go:build darwin

package platform

import "testing"

func TestLsofConnectionFields(t *testing.T) {
	rows := parseConnections("p123\nf6\nn192.0.2.1:1234->203.0.113.1:443\nTST=ESTABLISHED\nf7\nn127.0.0.1:7777->127.0.0.1:5555\nTST=ESTABLISHED\nf8\nn*:8888\nTST=LISTEN\n")
	if len(rows) != 1 || rows[0].IP != "203.0.113.1" || rows[0].State != "ESTABLISHED" {
		t.Fatal(rows)
	}
}
