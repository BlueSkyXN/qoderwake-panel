package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"time"

	legacyops "qoderwake-panel/ops"
	legacycontracts "qoderwake-panel/panel"
)

// Legacy 保留已登记的 Linux Python 网关事务，面板的 HTTP 服务始终由 Go 提供。
// 新建实例使用 Native Manager；已有 schema1 网关延续原 runtime 与回退链。
type Legacy struct {
	Root, ConfigPath, Upstream, User, Python string
	Port                                     int
}

func IsLegacy(root string) bool {
	raw, err := regular(filepath.Join(root, "gateway-runtime", "current.json"), 1024*1024)
	if err != nil {
		return false
	}
	var data struct {
		Version int `json:"schemaVersion"`
	}
	return json.Unmarshal(raw, &data) == nil && data.Version == 1
}
func legacySources() (map[string][]byte, error) {
	files := map[string][]byte{}
	for _, name := range []string{"gateway-manager.py", "uplink-gw.py"} {
		raw, err := legacyops.Files.ReadFile(name)
		if err != nil {
			return nil, err
		}
		files["ops/"+name] = raw
	}
	for _, name := range []string{"gateway_policy.py", "gateway_runtime.py"} {
		raw, err := legacycontracts.Files.ReadFile(name)
		if err != nil {
			return nil, err
		}
		files["panel/"+name] = raw
	}
	return files, nil
}
func (l Legacy) helper() (string, error) {
	files, err := legacySources()
	if err != nil {
		return "", err
	}
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	hash := sha256.New()
	for _, name := range names {
		hash.Write([]byte(name + "\x00"))
		hash.Write(files[name])
	}
	directory := filepath.Join(l.Root, "compat", "gateway-"+hex.EncodeToString(hash.Sum(nil))[:24])
	for _, part := range []string{"ops", "panel"} {
		if err = os.MkdirAll(filepath.Join(directory, part), 0700); err != nil {
			return "", err
		}
		dir, e := openDirectory(filepath.Join(directory, part))
		if e != nil {
			return "", e
		}
		dir.Close()
	}
	for _, name := range names {
		path := filepath.Join(directory, name)
		current, e := regular(path, 4*1024*1024)
		if e == nil {
			currentHash := sha256.Sum256(current)
			expected := sha256.Sum256(files[name])
			if currentHash != expected {
				return "", errors.New("legacy_gateway_helper_changed")
			}
			continue
		}
		if !os.IsNotExist(e) {
			return "", e
		}
		if err = writeAtomic(path, files[name], 0600, uint32(os.Getegid())); err != nil {
			return "", err
		}
	}
	return filepath.Join(directory, "ops", "gateway-manager.py"), nil
}
func (l Legacy) Call(ctx context.Context, action string, p *Policy) (map[string]any, error) {
	if runtime.GOOS != "linux" {
		return nil, errors.New("legacy_gateway_platform_unsupported")
	}
	if action != "status" && action != "apply" && action != "preflight" && action != "rollback" && action != "stop" {
		return nil, errors.New("invalid_gateway_action")
	}
	if !ApprovedUpstream(l.Upstream) || l.Port < 1 || l.Port > 65535 || l.User == "" || filepath.Dir(l.ConfigPath) != filepath.Join(l.Root, "config") {
		return nil, errors.New("invalid_gateway_configuration")
	}
	python := l.Python
	var err error
	if python == "" {
		python, err = exec.LookPath("python3")
		if err != nil {
			return nil, errors.New("legacy_gateway_python_unavailable")
		}
	}
	helper, err := l.helper()
	if err != nil {
		return nil, err
	}
	env := []string{"PATH=/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin", "PYTHONDONTWRITEBYTECODE=1", "QW_ROOT=" + l.Root, "QW_GW_CONFIG=" + l.ConfigPath, "QW_GW_PORT=" + strconv.Itoa(l.Port), "QW_GW_USER=" + l.User, "QW_GW_UPSTREAM=" + l.Upstream}
	if p != nil {
		if err = p.Validate(); err != nil {
			return nil, err
		}
		file, err := os.CreateTemp(l.Root, ".gateway-candidate-")
		if err != nil {
			return nil, err
		}
		defer os.Remove(file.Name())
		raw, _ := json.Marshal(p)
		_, err = file.Write(raw)
		closeErr := file.Close()
		if err != nil {
			return nil, err
		}
		if closeErr != nil {
			return nil, closeErr
		}
		env = append(env, "QW_GW_CANDIDATE="+file.Name())
	}
	timeout := 90 * time.Second
	if action == "status" {
		timeout = 5 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, python, "-B", helper, action)
	cmd.Env = env
	cmd.Dir = l.Root
	out := &limitedBuffer{max: 65536}
	cmd.Stdout = out
	cmd.Stderr = io.Discard
	err = cmd.Run()
	var result map[string]any
	if json.Unmarshal(out.Bytes(), &result) != nil {
		return nil, errors.New("legacy_gateway_manager_failed")
	}
	if err != nil || result["ok"] != true {
		code, _ := result["error"].(string)
		if !regexp.MustCompile(`^[a-z][a-z0-9_]{1,119}$`).MatchString(code) {
			code = "legacy_gateway_manager_failed"
		}
		return nil, errors.New(code)
	}
	return result, nil
}
