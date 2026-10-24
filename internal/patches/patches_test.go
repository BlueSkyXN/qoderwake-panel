package patches

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fixture(t *testing.T) *Manager {
	t.Helper()
	root := t.TempDir()
	root, _ = filepath.EvalSymlinks(root)
	binary := filepath.Join(root, "qoderwake")
	content := append([]byte("synthetic executable:"), original...)
	if err := os.WriteFile(binary, content, 0750); err != nil {
		t.Fatal(err)
	}
	p := profile{"module": "qcs-endpoint", "version": "fixture-cn", "original_sha256": digest(content), "patched_sha256": digest(bytes.Replace(content, original, replacement, 1))}
	registry, _ := json.Marshal(map[string]any{"profiles": []profile{p}})
	m := New(root, binary, "", registry, 19840)
	m.stopped = func(context.Context, string) error { return nil }
	return m
}
func TestPatchApplyRestoreAndPythonProfileCompatibility(t *testing.T) {
	m := fixture(t)
	originalData, _ := os.ReadFile(m.Binary)
	p, err := m.Plan("apply", "owner")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.Execute(context.Background(), p["id"].(string), "other"); err == nil {
		t.Fatal("foreign principal accepted")
	}
	r, err := m.Execute(context.Background(), p["id"].(string), "owner")
	if err != nil || r["state"] != "patched" {
		t.Fatal(r, err)
	}
	if _, err = m.Execute(context.Background(), p["id"].(string), "owner"); err == nil {
		t.Fatal("consumed plan replayed")
	}
	if info, _ := os.Stat(m.Binary); info.Mode().Perm() != 0750 {
		t.Fatal("file mode changed")
	}
	registry := filepath.Join(m.Root, "registry.json")
	os.WriteFile(registry, m.DefaultRegistry, 0600)
	if py, e := exec.LookPath("python3"); e == nil {
		reference, _ := filepath.Abs("../../panel")
		script := `import json,sys
from pathlib import Path
sys.path.insert(0,sys.argv[1])
from panel_patches import PatchManager
m=PatchManager(*sys.argv[2:],19840)
print(json.dumps(m.status()))`
		out, e := exec.Command(py, "-c", script, reference, m.Root, m.Binary, registry).Output()
		if e != nil {
			t.Fatal("reference inspect", e)
		}
		var state map[string]any
		if json.Unmarshal(out, &state) != nil || state["state"] != "patched" || state["sha256"] != r["sha256"] {
			t.Fatal("Python did not recognize Go patch")
		}
	}
	p, err = m.Plan("restore", "owner")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.Execute(context.Background(), p["id"].(string), "owner"); err != nil {
		t.Fatal(err)
	}
	actual, _ := os.ReadFile(m.Binary)
	if !bytes.Equal(actual, originalData) {
		t.Fatal("restore changed original bytes")
	}
}
func TestPatchStalePlanBackupAndSymlinks(t *testing.T) {
	for _, scenario := range []string{"changed", "expired", "backup", "symlink"} {
		t.Run(scenario, func(t *testing.T) {
			m := fixture(t)
			p, err := m.Plan("apply", "owner")
			if err != nil {
				t.Fatal(err)
			}
			token := p["id"].(string)
			switch scenario {
			case "changed":
				os.WriteFile(m.Binary, []byte("replacement"), 0750)
			case "expired":
				item := m.plans[token]
				item.Expires = time.Now().Add(-time.Second)
				m.plans[token] = item
			case "backup":
				os.Mkdir(filepath.Join(m.Root, "patches"), 0700)
				item := m.plans[token]
				os.WriteFile(filepath.Join(m.Root, "patches", item.Profile.text("original_sha256")+".bin"), []byte("bad"), 0600)
			case "symlink":
				other := t.TempDir()
				os.Symlink(other, filepath.Join(m.Root, "patches"))
			}
			before, _ := os.ReadFile(m.Binary)
			if _, err = m.Execute(context.Background(), token, "owner"); err == nil {
				t.Fatal("invalid transaction accepted")
			}
			after, _ := os.ReadFile(m.Binary)
			if !bytes.Equal(before, after) {
				t.Fatal("rejected transaction modified binary")
			}
		})
	}
}
func TestPatchLegacyBackupRestoreAndUnregisteredHash(t *testing.T) {
	m := fixture(t)
	data, _ := os.ReadFile(m.Binary)
	os.WriteFile(m.Binary+".bak-p1-fixture", data, 0700)
	os.WriteFile(m.Binary, bytes.Replace(data, original, replacement, 1), 0700)
	p, err := m.Plan("restore", "owner")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.Execute(context.Background(), p["id"].(string), "owner"); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(m.Binary, []byte("unregistered"), 0700)
	if _, err = m.Plan("apply", "owner"); err == nil || !strings.Contains(err.Error(), "unsupported_binary_hash") {
		t.Fatal("unknown version accepted")
	}
}
