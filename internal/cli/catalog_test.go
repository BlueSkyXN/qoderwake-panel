package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"qoderwake-panel/internal/apierror"
	"qoderwake-panel/internal/auth"
	"reflect"
	"testing"
)

func TestAll373ContractsMatchPython(t *testing.T) {
	catalog, e := NewCatalog()
	if e != nil {
		t.Fatal(e)
	}
	if len(catalog.Specs) != 373 {
		t.Fatalf("operation count %d", len(catalog.Specs))
	}
	py, e := exec.LookPath("python3")
	if e != nil {
		t.Skip("Python reference runtime unavailable")
	}
	path, _ := filepath.Abs("../../panel")
	cmd := exec.Command(py, "-c", `import json,sys
sys.path.insert(0,sys.argv[1])
from cli_catalog import Catalog
print(json.dumps(list(Catalog().operations.values()),ensure_ascii=False))`, path)
	cmd.Env = append(os.Environ(), "PYTHONDONTWRITEBYTECODE=1")
	reference, e := cmd.Output()
	if e != nil {
		t.Fatal("reference catalog failed", e)
	}
	actual, _ := json.Marshal(catalog.Specs)
	var a, b any
	if json.Unmarshal(actual, &a) != nil || json.Unmarshal(reference, &b) != nil {
		t.Fatal("invalid reference JSON")
	}
	if !reflect.DeepEqual(a, b) {
		aa := a.([]any)
		bb := b.([]any)
		for i := range aa {
			if !reflect.DeepEqual(aa[i], bb[i]) {
				t.Fatalf("contract mismatch for %s", catalog.Specs[i].ID)
			}
		}
		t.Fatal("catalog mismatch")
	}
}
func TestArgvIsDataAndScopeCannotExpand(t *testing.T) {
	c, e := NewCatalog()
	if e != nil {
		t.Fatal(e)
	}
	spec, e := c.Get("mcp.add")
	if e != nil {
		t.Fatal(e)
	}
	p := Parameters{Options: map[string]any{"--name": "test; echo unsafe", "--waker-id": "w1"}}
	argv, e := BuildArgv(spec, p)
	if e != nil {
		t.Fatal(e)
	}
	if !bytes.Contains([]byte(argv[2]), []byte("test; echo unsafe")) {
		found := false
		for _, v := range argv {
			found = found || v == "--name=test; echo unsafe"
		}
		if !found {
			t.Fatal("argument was changed")
		}
	}
	identity := auth.Identity{UserID: "editor", Role: "maintainer", Wakers: []string{"w1"}}
	p.Options["--json"] = "{}"
	e = c.Authorize(identity, spec, &p)
	var ae *apierror.Error
	if !errors.As(e, &ae) || ae.Status != 403 {
		t.Fatal("scoped arbitrary JSON accepted", e)
	}
	p.Options = map[string]any{"--unexpected": true}
	_, e = BuildArgv(spec, p)
	if !errors.As(e, &ae) || ae.Code != "unknown_cli_parameter" {
		t.Fatal(e)
	}
}
func TestOptionalAndVariadicArgumentRules(t *testing.T) {
	spec := Spec{Path: []string{"example"}, Arguments: []Argument{{Name: "optional"}, {Name: "required", Required: true}}, Options: []Option{{Name: "--flag"}, {Name: "--values", Value: true, Variadic: true}}}
	_, e := BuildArgv(spec, Parameters{Arguments: map[string]any{"required": "value"}})
	if e == nil {
		t.Fatal("positional gap allowed")
	}
	_, e = BuildArgv(spec, Parameters{Options: map[string]any{"--flag": "false"}})
	if e == nil {
		t.Fatal("string boolean accepted")
	}
	_, e = BuildArgv(spec, Parameters{Options: map[string]any{"--values": []any{"first", "--injected"}}})
	if e == nil {
		t.Fatal("variadic flag accepted")
	}
}
