package managed

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"qoderwake-panel/internal/platform"
)

func legacyState() State {
	file := platform.File{Path: "/synthetic/qoderwake", Device: 1, Inode: 2}
	return State{SchemaVersion: 1, Name: "daemon-cn", Profile: "daemon", Status: "running", PID: 123, StartTicks: 400, BootID: "00000000-0000-0000-0000-000000000001", UID: 501, Executable: file, Launch: File{file, strings.Repeat("0", 64)}, Cmdline: []string{file.Path, "start", "--foreground"}, Home: "/synthetic/home", Port: 19830, Mode: "direct", Environment: map[string]string{"QODERWAKE_HOME": "/synthetic/home"}}
}
func TestLegacyProcessRecordMatchesPython(t *testing.T) {
	state := legacyState()
	raw, e := json.Marshal(state)
	if e != nil {
		t.Fatal(e)
	}
	py, e := exec.LookPath("python3")
	if e != nil {
		t.Skip("Python reference unavailable")
	}
	source, _ := filepath.Abs("../../ops/process-control.py")
	command := exec.Command(py, "-c", `import importlib.util,json,sys
spec=importlib.util.spec_from_file_location('process_control',sys.argv[1]);m=importlib.util.module_from_spec(spec);spec.loader.exec_module(m)
print(json.dumps(m.validate_state(json.load(sys.stdin))))`, source)
	command.Stdin = bytes.NewReader(raw)
	output, e := command.Output()
	if e != nil {
		t.Fatal("legacy validator rejected Go record", e)
	}
	var expected, actual any
	json.Unmarshal(output, &expected)
	json.Unmarshal(raw, &actual)
	if !reflect.DeepEqual(expected, actual) {
		t.Fatal("legacy record shape differs")
	}
	path := filepath.Join(t.TempDir(), "state.json")
	os.WriteFile(path, raw, 0600)
	loaded, e := Read(path)
	if e != nil || !reflect.DeepEqual(*loaded, state) {
		t.Fatal("legacy record read failed", e)
	}
}
func TestProcessRecordRejectsMissingAndUnexpectedFields(t *testing.T) {
	raw, _ := json.Marshal(legacyState())
	var value map[string]any
	json.Unmarshal(raw, &value)
	path := filepath.Join(t.TempDir(), "state.json")
	delete(value, "uid")
	bad, _ := json.Marshal(value)
	os.WriteFile(path, bad, 0600)
	if _, e := Read(path); e == nil {
		t.Fatal("missing required uid accepted")
	}
	state := legacyState()
	state.Cmdline = append(state.Cmdline, "--unsafe")
	if state.Validate() == nil {
		t.Fatal("unexpected command accepted")
	}
	state = legacyState()
	state.Environment["UNTRACKED_SECRET"] = "synthetic"
	if state.Validate() == nil {
		t.Fatal("untracked environment persisted")
	}
	state = legacyState()
	state.SchemaVersion = 2
	state.Platform = "darwin"
	state.Environment["HTTPS_PROXY"] = "http://127.0.0.1:18888"
	state.Supervisor = &Supervisor{Kind: "launchd", Label: "com.qoderwake.panel.example", Path: "/synthetic/agent.plist", SHA256: strings.Repeat("a", 64)}
	if state.Validate() != nil {
		t.Fatal("native Mac supervisor state rejected")
	}
	state.SchemaVersion = 1
	if state.Validate() == nil {
		t.Fatal("native fields accepted in legacy schema")
	}
}
