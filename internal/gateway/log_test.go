package gateway

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
)

func TestLogRotationAndLegacyTailWindow(t *testing.T) {
	root := t.TempDir()
	root, _ = filepath.EvalSymlinks(root)
	l := NewLogger(root)
	l.limit = 200
	if err := l.Prepare(); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		if err := l.Append(Record{Time: "2026-10-24T00:00:00Z", Method: "POST", Path: "/<redacted>", Status: 204, Action: "sink"}); err != nil {
			t.Fatal(err)
		}
	}
	paths := []string{l.Path, l.Path + ".1", l.Path + ".2", l.Path + ".3"}
	window, err := Tail(paths, 5000, 1024*1024)
	if err != nil || window.Sources != 4 || len(window.Lines) != 4 {
		t.Fatal("rotation window", window, err)
	}
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatal("log mode", err)
		}
	}
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("Python reference unavailable")
	}
	reference, _ := filepath.Abs("../../panel")
	pathsJSON, _ := json.Marshal(paths)
	raw, err := exec.Command(py, "-c", `import json,sys;sys.path.insert(0,sys.argv[1]);from log_io import tail_records;print(json.dumps(tail_records(json.loads(sys.argv[2]),max_lines=2,max_bytes=400)))`, reference, string(pathsJSON)).Output()
	if err != nil {
		t.Fatal(err)
	}
	var old map[string]any
	json.Unmarshal(raw, &old)
	actual, err := Tail(paths, 2, 400)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ = json.Marshal(actual)
	var converted map[string]any
	json.Unmarshal(raw, &converted)
	delete(converted, "available")
	oldLines := old["lines"]
	delete(old, "lines")
	if !reflect.DeepEqual(old, converted) {
		t.Fatal("tail metadata drift", old, converted)
	}
	raw, _ = json.Marshal(actual.Lines)
	var lines any
	json.Unmarshal(raw, &lines)
	if !reflect.DeepEqual(oldLines, lines) {
		t.Fatal("tail line order drift")
	}
}
func TestTailPartialUTF8AndSpecialFiles(t *testing.T) {
	root := t.TempDir()
	root, _ = filepath.EvalSymlinks(root)
	path := filepath.Join(root, "log")
	os.WriteFile(path, []byte("first\nsecond\npartial"), 0600)
	w, err := Tail([]string{path}, 1, 1024)
	if err != nil || len(w.Lines) != 1 || w.Lines[0] != "second" || !w.Truncated {
		t.Fatal(w, err)
	}
	for _, kind := range []string{"symlink", "hardlink", "fifo"} {
		target := filepath.Join(root, kind)
		switch kind {
		case "symlink":
			os.Symlink(path, target)
		case "hardlink":
			os.Link(path, target)
		case "fifo":
			syscall.Mkfifo(target, 0600)
		}
		if _, err := Tail([]string{target}, 10, 1024); err == nil {
			t.Fatal("special file accepted", kind)
		}
	}
}
func TestLoggerRejectsSymlinkAndDoesNotLeakBody(t *testing.T) {
	root := t.TempDir()
	root, _ = filepath.EvalSymlinks(root)
	l := NewLogger(root)
	if err := l.Prepare(); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(root, "outside")
	os.WriteFile(out, []byte("original"), 0600)
	os.Remove(l.Path)
	os.Symlink(out, l.Path)
	if err := l.Append(Record{Path: "/<redacted>"}); err == nil {
		t.Fatal("log symlink accepted")
	}
	raw, _ := os.ReadFile(out)
	if string(raw) != "original" {
		t.Fatal("symlink target changed")
	}
	os.Remove(l.Path)
	if err := l.Append(Record{Path: strings.Repeat("x", maxRecord)}); err == nil {
		t.Fatal("oversized record accepted")
	}
}
