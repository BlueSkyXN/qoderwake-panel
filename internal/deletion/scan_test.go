package deletion

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
)

func TestExistingPythonDeletionScenariosMatch(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("Python reference runtime unavailable")
	}
	tests, _ := filepath.Abs("../../tests")
	script := `import copy,io,json,sys,unittest
sys.path.insert(0,sys.argv[1])
import test_deletion_preflight as reference
original=reference.DeletionPreflight.scan
cases=[]
class Recorder:
 def __init__(self,source,calls):self.source,self.calls=source,calls
 def __getattr__(self,name):
  function=getattr(self.source,name)
  def invoke(*args):
   key=name+'|'+(str(args[0]) if args and args[0] is not None else '')
   try:value=function(*args)
   except Exception:
    self.calls[key]={'error':True}
    raise
   self.calls[key]={'value':copy.deepcopy(value)}
   return value
  return invoke
def capture(self,kind,target,revision=''):
 calls={}
 scanner=reference.DeletionPreflight(Recorder(self.source,calls))
 result=original(scanner,kind,target,revision)
 cases.append({'kind':kind,'target':target,'revision':revision,'calls':calls,'expected':copy.deepcopy(result)})
 return result
reference.DeletionPreflight.scan=capture
suite=unittest.defaultTestLoader.loadTestsFromModule(reference)
result=unittest.TextTestRunner(stream=io.StringIO()).run(suite)
if not result.wasSuccessful():raise RuntimeError('reference tests failed')
print(json.dumps(cases,ensure_ascii=False))`
	cmd := exec.Command(python, "-c", script, tests)
	cmd.Env = append(os.Environ(), "PYTHONDONTWRITEBYTECODE=1")
	output, err := cmd.Output()
	if err != nil {
		t.Fatal("reference execution failed", err)
	}
	var cases []struct {
		Kind, Target, Revision string
		Calls                  map[string]struct {
			Error bool
			Value any
		}
		Expected Impact
	}
	decoder := json.NewDecoder(bytes.NewReader(output))
	decoder.UseNumber()
	if err = decoder.Decode(&cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) < 10 {
		t.Fatal("reference scenarios missing")
	}
	for index, c := range cases {
		t.Run(fmt.Sprintf("%02d_%s", index, c.Kind), func(t *testing.T) {
			scanner := Scanner{Source: func(name, target string) (any, error) {
				call, ok := c.Calls[name+"|"+target]
				if !ok || call.Error {
					return nil, errors.New("unavailable")
				}
				return call.Value, nil
			}}
			actual, err := scanner.Scan(c.Kind, c.Target, c.Revision)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(actual, c.Expected) {
				a, _ := json.MarshalIndent(actual, "", "  ")
				b, _ := json.MarshalIndent(c.Expected, "", "  ")
				t.Fatalf("scan differs from reference\nGo: %s\nPython: %s", a, b)
			}
		})
	}
}
func TestUnknownRequiredSourcesNeverPermitDeletion(t *testing.T) {
	scanner := Scanner{Source: func(name, target string) (any, error) { return nil, errors.New("unavailable") }}
	for _, kind := range []string{"waker", "provider"} {
		impact, err := scanner.Scan(kind, "example", "")
		if err != nil {
			t.Fatal(err)
		}
		if impact.Deletable || impact.Completeness != "unavailable" {
			t.Fatal("unknown sources allowed deletion")
		}
	}
}
