package gateway

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func TestLegacyHelperPreservesEmbeddedSourcesAndRejectsChanges(t *testing.T) {
	root := t.TempDir()
	root, _ = filepath.EvalSymlinks(root)
	l := Legacy{Root: root}
	helper, err := l.helper()
	if err != nil {
		t.Fatal(err)
	}
	files, err := legacySources()
	if err != nil {
		t.Fatal(err)
	}
	for name, expected := range files {
		path := filepath.Join(filepath.Dir(filepath.Dir(helper)), name)
		raw, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(raw, expected) {
			t.Fatal("source changed", name, err)
		}
	}
	if py, err := exec.LookPath("python3"); err == nil {
		out, err := exec.Command(py, "-B", helper, "--help").Output()
		if err != nil || !bytes.Contains(out, []byte("preflight")) {
			t.Fatal("legacy CLI load failed", err)
		}
	}
	if err = os.WriteFile(helper, []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = l.helper(); err == nil {
		t.Fatal("modified helper overwritten or accepted")
	}
	if runtime.GOOS != "linux" {
		if _, err = l.Call(context.Background(), "status", nil); err == nil || err.Error() != "legacy_gateway_platform_unsupported" {
			t.Fatal("legacy Linux helper executed on Mac", err)
		}
	}
}
