package managed

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unicode/utf8"
)

var Switches = map[string]string{"hotDeploy": "QODERWAKE_HOT_DEPLOY", "embeddingDisabled": "QODER_MEMORY_DISABLE_EMBEDDING", "sdkByok": "QODER_SDK_CUSTOM_BASE_URL_BYOK", "sessionProjectionUplink": "QODERWAKE_SESSION_PROJECTION_UPLINK", "remoteExecutionUplink": "QODERWAKE_REMOTE_EXECUTION_UPLINK"}
var ErrPolicy = errors.New("invalid_runtime_policy")

type Policy map[string]any

func (p Policy) Validate() error {
	for _, key := range []string{"hotDeploy", "embeddingDisabled"} {
		if _, ok := p[key].(bool); !ok {
			return ErrPolicy
		}
	}
	for key, value := range p {
		if _, ok := Switches[key]; !ok {
			return ErrPolicy
		}
		if value == nil && (key == "sessionProjectionUplink" || key == "remoteExecutionUplink") {
			continue
		}
		if _, ok := value.(bool); !ok {
			return ErrPolicy
		}
	}
	return nil
}
func ReadPolicy(root string) (Policy, error) {
	file, err := os.OpenFile(filepath.Join(root, "runtime-policy.json"), os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if os.IsNotExist(err) {
		return Policy{"hotDeploy": false, "embeddingDisabled": true}, nil
	}
	if err != nil {
		return nil, ErrPolicy
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, ErrPolicy
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || stat.Nlink != 1 || info.Size() > 4096 {
		return nil, ErrPolicy
	}
	raw, err := io.ReadAll(io.LimitReader(file, 4097))
	if err != nil || len(raw) > 4096 || !utf8.Valid(raw) {
		return nil, ErrPolicy
	}
	var policy Policy
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if decoder.Decode(&policy) != nil || policy == nil || decoder.Decode(new(any)) != io.EOF || policy.Validate() != nil {
		return nil, ErrPolicy
	}
	return policy, nil
}
func Atomic(path string, raw []byte) error {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0700); err != nil {
		return err
	}
	if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
		return errors.New("managed_path_not_regular")
	}
	file, err := os.CreateTemp(directory, ".managed-")
	if err != nil {
		return err
	}
	name := file.Name()
	defer os.Remove(name)
	if _, err = file.Write(raw); err != nil {
		file.Close()
		return err
	}
	if err = file.Chmod(0600); err != nil {
		file.Close()
		return err
	}
	if err = file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	if err = os.Rename(name, path); err != nil {
		return err
	}
	dir, err := os.Open(directory)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
func SavePolicy(root string, policy Policy) error {
	if err := policy.Validate(); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(policy, "", "  ")
	if err != nil {
		return err
	}
	return Atomic(filepath.Join(root, "runtime-policy.json"), raw)
}
func Switch(value string, present bool) (any, error) {
	if !present {
		return nil, nil
	}
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "on":
		return true, nil
	case "0", "false", "off":
		return false, nil
	}
	return nil, errors.New("invalid_runtime_switch")
}
func (p Policy) Environment(base map[string]string) (map[string]string, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	out := map[string]string{}
	for key, value := range base {
		out[key] = value
	}
	for key, variable := range Switches {
		value, exists := p[key]
		if key == "sdkByok" && !exists {
			value = true
		}
		if value != nil {
			if value == true {
				out[variable] = "1"
			} else {
				out[variable] = "0"
			}
		}
	}
	return out, nil
}
func (p Policy) Effective(base map[string]string) (map[string]any, error) {
	env, err := p.Environment(base)
	if err != nil {
		return nil, err
	}
	out := map[string]any{}
	for key, variable := range Switches {
		value, exists := env[variable]
		out[key], err = Switch(value, exists)
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}
func WriteState(path string, state *State) error {
	if err := state.Validate(); err != nil {
		return err
	}
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return Atomic(path, raw)
}
