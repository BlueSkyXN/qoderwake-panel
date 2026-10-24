package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"qoderwake-panel/internal/managed"
	"qoderwake-panel/internal/platform"
)

func TestFileHashBaselineAndSpecialFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "binary")
	if err := os.WriteFile(path, []byte("known-content"), 0700); err != nil {
		t.Fatal(err)
	}
	file, err := platform.FileIdentity(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("known-content"))
	record := &managed.File{File: file, SHA256: hex.EncodeToString(sum[:])}
	row := hashFile(context.Background(), path, record)
	if row["sameFile"] != true || row["matchesRecorded"] != true {
		t.Fatal("baseline mismatch", row)
	}
	if err := os.WriteFile(path, []byte("changed-content"), 0700); err != nil {
		t.Fatal(err)
	}
	row = hashFile(context.Background(), path, record)
	if row["sameFile"] != true || row["matchesRecorded"] != false {
		t.Fatal("content change missed", row)
	}
	link := path + "-link"
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	fifo := path + "-fifo"
	if err := syscall.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{link, fifo} {
		if row := hashFile(context.Background(), target, nil); row["present"] != false {
			t.Fatal("special file hashed")
		}
	}
}
func TestFileCheckHTTPAndMaintenanceProjection(t *testing.T) {
	s, _ := testServer(t)
	raw, csrf := login(t, s, "owner")
	if w := request(t, s, "POST", "/api/file-check", raw, csrf, map[string]any{}); w.Code != 409 {
		t.Fatal(w.Code)
	}
	if w := request(t, s, "POST", "/api/file-check", raw, csrf, map[string]any{"confirm": "/api/file-check"}); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	deadline := time.Now().Add(10 * time.Second)
	for s.fileCheckStatus()["status"] == "running" {
		if time.Now().After(deadline) {
			t.Fatal("hash check timed out")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if s.fileCheckStatus()["status"] != "done" {
		t.Fatal("hash check did not finish")
	}
	if _, err := s.Auth.CreateUser("reader", "Reader", "observer", nil, "example-password-for-tests", false); err != nil {
		t.Fatal(err)
	}
	reader, token := login(t, s, "reader")
	if w := request(t, s, "GET", "/api/file-check", reader, token, nil); w.Code != 403 {
		t.Fatal("file paths exposed")
	}
	request(t, s, "POST", "/api/maintenance/enter", raw, csrf, map[string]any{"confirm": "/api/maintenance/enter", "reason": "private operator details", "ttl": 30})
	w := request(t, s, "GET", "/api/maintenance", reader, token, nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"mode":"maintenance"`) || strings.Contains(w.Body.String(), "private") || strings.Contains(w.Body.String(), "activeCalls") {
		t.Fatal("maintenance projection", w.Code, w.Body.String())
	}
}
