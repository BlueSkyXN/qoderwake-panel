package jsoncompat

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
)

func TestPythonRevisionEncoding(t *testing.T) {
	py, e := exec.LookPath("python3")
	if e != nil {
		t.Skip("Python reference unavailable")
	}
	value := map[string]any{"name": "中文 <&> 😀 \u007f", "text": "actual\u2028line\u2029end; literal\\u2028\\u2029", "numbers": json.Number("9007199254740995"), "empty": nil}
	input, _ := json.Marshal(value)
	for _, mode := range []string{"spaced", "compact", "ascii"} {
		cmd := exec.Command(py, "-c", `import json,sys
x=json.load(sys.stdin)
mode=sys.argv[1]
print(json.dumps(x,sort_keys=True,ensure_ascii=mode=='ascii',**({} if mode=='spaced' else {'separators':(',',':')})),end='')`, mode)
		cmd.Stdin = strings.NewReader(string(input))
		expected, e := cmd.Output()
		if e != nil {
			t.Fatal(e)
		}
		actual := Marshal(value)
		if mode != "spaced" {
			actual = Compact(value, mode == "ascii")
		}
		if string(actual) != string(expected) {
			t.Fatalf("Python encoding differs in %s mode", mode)
		}
	}
}
