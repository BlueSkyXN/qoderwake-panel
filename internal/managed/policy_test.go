package managed

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRuntimePolicyDefaultsInheritanceAndPreservation(t *testing.T) {
	root := t.TempDir()
	policy, err := ReadPolicy(root)
	if err != nil {
		t.Fatal(err)
	}
	base := map[string]string{"QODERWAKE_HOT_DEPLOY": "1", "QODER_MEMORY_DISABLE_EMBEDDING": "0", "QODERWAKE_SESSION_PROJECTION_UPLINK": "off", "HTTPS_PROXY": "http://127.0.0.1:8888", "QODER_CONFIG_SERVICE_URL": "http://127.0.0.1:19840"}
	env, err := policy.Environment(base)
	if err != nil {
		t.Fatal(err)
	}
	if env["QODERWAKE_HOT_DEPLOY"] != "0" || env["QODER_MEMORY_DISABLE_EMBEDDING"] != "1" || env["QODER_SDK_CUSTOM_BASE_URL_BYOK"] != "1" || env["HTTPS_PROXY"] != base["HTTPS_PROXY"] || env["QODER_CONFIG_SERVICE_URL"] != base["QODER_CONFIG_SERVICE_URL"] {
		t.Fatal("saved switches or instance endpoints changed")
	}
	value, err := policy.Effective(base)
	if err != nil || value["sessionProjectionUplink"] != false || value["remoteExecutionUplink"] != nil {
		t.Fatal("uplink inheritance lost", err)
	}
	policy["sdkByok"] = false
	policy["sessionProjectionUplink"] = nil
	if err = SavePolicy(root, policy); err != nil {
		t.Fatal(err)
	}
	loaded, err := ReadPolicy(root)
	if err != nil || loaded["sdkByok"] != false {
		t.Fatal("saved policy not restored")
	}
	info, _ := os.Stat(filepath.Join(root, "runtime-policy.json"))
	if info.Mode().Perm() != 0600 {
		t.Fatal("policy mode is not private")
	}
	if _, exists := base["QODER_SDK_CUSTOM_BASE_URL_BYOK"]; exists {
		t.Fatal("source environment mutated")
	}
}
func TestRuntimePolicyRejectsMalformedAndLinkedSources(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "runtime-policy.json")
	for _, raw := range []string{`{}`, `{"hotDeploy":false,"embeddingDisabled":"yes"}`, `{"hotDeploy":false,"embeddingDisabled":true,"sdkByok":null}`, `{"hotDeploy":false,"embeddingDisabled":true,"unknown":1}`} {
		os.WriteFile(path, []byte(raw), 0600)
		if _, err := ReadPolicy(root); err == nil {
			t.Fatal("invalid policy accepted")
		}
	}
	os.Remove(path)
	source := filepath.Join(t.TempDir(), "policy.json")
	os.WriteFile(source, []byte(`{"hotDeploy":false,"embeddingDisabled":true}`), 0600)
	os.Symlink(source, path)
	if _, err := ReadPolicy(root); err == nil {
		t.Fatal("policy symlink read")
	}
	if SavePolicy(root, Policy{"hotDeploy": false, "embeddingDisabled": true}) == nil {
		t.Fatal("policy symlink replaced")
	}
}
