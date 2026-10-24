package service

import (
	"encoding/xml"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestServiceArgumentsEscapedAndOfficialLabelRejected(t *testing.T) {
	c := Config{Platform: "darwin", Executable: "/Applications/Panel & Tools/panel", Root: "/example-user/Panel ' " + `"` + " % $root", Home: "/example-user/Daemon", Bind: "127.0.0.1", Port: 19841, Environment: map[string]string{"QW_ALLOWED_CLIENTS": "127.0.0.1,::1"}}
	raw, err := c.Render()
	if err != nil {
		t.Fatal(err)
	}
	decoder := xml.NewDecoder(strings.NewReader(string(raw)))
	values := []string{}
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if start, ok := token.(xml.StartElement); ok && start.Name.Local == "string" {
			var value string
			if err := decoder.DecodeElement(&value, &start); err != nil {
				t.Fatal(err)
			}
			values = append(values, value)
		}
	}
	if !strings.Contains(strings.Join(values, "\n"), c.Root) {
		t.Fatal("XML argument changed")
	}
	c.Platform = "linux"
	raw, err = c.Render()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "%% $$root") || !strings.Contains(string(raw), `\"`) {
		t.Fatal("systemd expansion not escaped")
	}
	c.Label = "com.qoder.QoderWake"
	if _, err = c.Render(); err == nil {
		t.Fatal("official service label accepted")
	}
}
func TestWritePreservesExistingService(t *testing.T) {
	path := filepath.Join(t.TempDir(), "service")
	if err := Write(path, []byte("first")); err != nil {
		t.Fatal(err)
	}
	if err := Write(path, []byte("second")); err == nil {
		t.Fatal("overwrote service")
	}
	raw, _ := os.ReadFile(path)
	if string(raw) != "first" {
		t.Fatal("existing file changed")
	}
}

func TestSystemdWorkingDirectoryLiteralPath(t *testing.T) {
	root := filepath.Join(t.TempDir(), `Panel ' " % $root \ `)
	c := Config{Platform: "linux", Executable: "/bin/true", Root: root, Home: "/tmp", Bind: "127.0.0.1", Port: 19841}
	raw, err := c.Render()
	if err != nil {
		t.Fatal(err)
	}
	want := "WorkingDirectory=" + strings.ReplaceAll(root, "%", "%%") + "/.\n"
	if !strings.Contains(string(raw), want) {
		t.Fatal("working directory no longer preserves the literal path")
	}
	if runtime.GOOS != "linux" {
		return
	}
	parser, err := exec.LookPath("systemd-analyze")
	if err != nil {
		t.Skip("systemd-analyze unavailable")
	}
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	units := t.TempDir()
	unit := filepath.Join(units, "panel-render-test.service")
	if err := Write(unit, raw); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(parser, "verify", "--man=no", unit)
	cmd.Env = append(os.Environ(), "SYSTEMD_UNIT_PATH="+units+":/usr/lib/systemd/system:/lib/systemd/system")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("systemd parser rejected rendered unit: %v\n%s", err, output)
	}
}
