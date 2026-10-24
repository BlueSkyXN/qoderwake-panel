package gateway

import (
	"encoding/json"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestPolicyLegacyDecisionsAndHash(t *testing.T) {
	raw := []byte(`{"mode":"strict","rules":[{"method":"GET","path":"/catalog","query":["page"],"headers":["accept"],"max_body":0}]}`)
	p, err := DecodePolicy(raw)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		Method, Path string
		Size         int64
		Want         string
	}{{"GET", "/catalog?page=1", 0, "forward"}, {"POST", "/catalog", 0, "deny"}, {"GET", "/catalog?token=x", 0, "deny"}, {"GET", "/catalog/extra", 0, "deny"}, {"GET", "/catalog", 1, "deny"}, {"GET", "//evil.test/catalog", 0, "deny"}, {"GET", "/%63atalog", 0, "deny"}, {"GET", "/a/../catalog", 0, "deny"}}
	for _, test := range cases {
		if got, _ := p.Decision(test.Method, test.Path, test.Size); got != test.Want {
			t.Fatal(test, got)
		}
	}
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("reference Python unavailable")
	}
	reference, _ := filepath.Abs("../../panel")
	output, err := exec.Command(py, "-c", `import sys,json;sys.path.insert(0,sys.argv[1]);from gateway_policy import validate_cfg,policy_hash;print(json.dumps({'hash':policy_hash(validate_cfg(json.loads(sys.argv[2])))}))`, reference, string(raw)).Output()
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]string
	if json.Unmarshal(output, &result) != nil || result["hash"] != p.Hash() {
		t.Fatal("policy hash incompatible")
	}
	enforce, err := DecodePolicy([]byte(`{"mode":"enforce","sink_post_prefixes":["/tracking"],"token_allow_prefixes":["/tracking/required"]}`))
	if err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]string{"/tracking": "sink", "/tracking/one": "sink", "/tracking-other": "forward", "/tracking/required/session": "forward"} {
		if got, _ := enforce.Decision("POST", path, 20); got != want {
			t.Fatal(path, got)
		}
	}
}
func TestInvalidPolicyNeverFallsBackToObserve(t *testing.T) {
	for _, raw := range []string{`null`, `{}`, `{"mdoe":"strict"}`, `{"mode":"observe","unknown":true}`, `{"mode":"observe","mode":null}`, `{"mode":"strict","rules":[{"path":"/"}]}`, `{"mode":"strict","rules":[{"method":"GET","path":"/","query":[],"headers":[],"max_body":null}]}`, `{"mode":"observe","audit_paths":null}`, `{"mode":"observe","rules":null}`, `{"mode":"observe","sink_post_prefixes":["/"]}`, `{"mode":"strict","token_policy":"strict"}`} {
		if _, err := DecodePolicy([]byte(raw)); err == nil {
			t.Fatal("invalid policy accepted", raw)
		}
	}
}
