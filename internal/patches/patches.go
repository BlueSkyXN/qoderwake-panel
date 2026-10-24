// Package patches preserves the frozen, hash-pinned CN binary patch workflow.
package patches

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const maximum = 512 * 1024 * 1024

var original = []byte(`"https://openapi.qoder.com.cn"`)
var replacement = []byte(`"http://qwgw.local.test:19840"`)
var hashPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

type profile map[string]any

func (p profile) text(key string) string { value, _ := p[key].(string); return value }

type plan struct {
	Action, Principal, Hash string
	Expires                 time.Time
	Profile                 profile
}
type Manager struct {
	Root, Binary, Registry string
	DefaultRegistry        []byte
	GatewayPort            int
	mu                     sync.Mutex
	plans                  map[string]plan
	stopped                func(context.Context, string) error
}

func New(root, binary, registry string, defaults []byte, port int) *Manager {
	if absolute, err := filepath.Abs(root); err == nil {
		root = absolute
	}
	if real, err := filepath.EvalSymlinks(root); err == nil {
		root = real
	}
	if absolute, err := filepath.Abs(binary); err == nil {
		binary = absolute
	}
	return &Manager{Root: root, Binary: binary, Registry: registry, DefaultRegistry: defaults, GatewayPort: port, plans: map[string]plan{}, stopped: requireStopped}
}
func digest(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }
func readFile(path string, limit int64) ([]byte, error) {
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return readOpen(file, limit)
}
func readOpen(file *os.File, limit int64) ([]byte, error) {
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > limit {
		return nil, errors.New("patch_backup_mismatch")
	}
	raw, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > limit {
		return nil, errors.New("patch_backup_mismatch")
	}
	return raw, nil
}
func (m *Manager) profiles() ([]profile, error) {
	raw := m.DefaultRegistry
	if m.Registry != "" {
		var err error
		raw, err = readFile(m.Registry, 1024*1024)
		if os.IsNotExist(err) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
	}
	var data struct {
		Profiles []profile `json:"profiles"`
	}
	if json.Unmarshal(raw, &data) != nil {
		return nil, errors.New("invalid_patch_registry")
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil || bytes.Equal(bytes.TrimSpace(fields["profiles"]), []byte("null")) {
		return nil, errors.New("invalid_patch_registry")
	}
	for _, row := range data.Profiles {
		if !hashPattern.MatchString(row.text("original_sha256")) || !hashPattern.MatchString(row.text("patched_sha256")) || row["original_sha256"] == row["patched_sha256"] {
			return nil, errors.New("invalid_patch_registry")
		}
	}
	return data.Profiles, nil
}
func (m *Manager) inspect() ([]byte, profile, string, error) {
	raw, err := readFile(m.Binary, maximum)
	if err != nil {
		return nil, nil, "", errors.New("patch_binary_unavailable")
	}
	hash := digest(raw)
	profiles, err := m.profiles()
	if err != nil {
		return nil, nil, "", err
	}
	for _, p := range profiles {
		if p.text("module") == "qcs-endpoint" && p.text("version") != "" {
			if hash == p.text("original_sha256") {
				return raw, p, "original", nil
			}
			if hash == p.text("patched_sha256") {
				return raw, p, "patched", nil
			}
		}
	}
	return raw, nil, "unsupported", nil
}
func (m *Manager) Status() map[string]any {
	raw, p, state, err := m.inspect()
	if err != nil {
		return map[string]any{"state": "unavailable", "verified": false, "platformSupported": runtime.GOOS == "linux"}
	}
	return map[string]any{"state": state, "sha256": digest(raw), "verified": p != nil, "version": p["version"], "module": p["module"], "modules": []map[string]string{{"id": "qcs-endpoint", "name": "QCS 本机网关端点"}}, "platformSupported": runtime.GOOS == "linux"}
}
func (m *Manager) Plan(action, principal string) (map[string]any, error) {
	if action != "apply" && action != "restore" {
		return nil, errors.New("invalid_patch_action")
	}
	if action == "apply" && m.GatewayPort != 19840 {
		return nil, errors.New("patch_requires_gateway_port_19840")
	}
	raw, p, state, err := m.inspect()
	if err != nil {
		return nil, err
	}
	if p == nil {
		return nil, errors.New("unsupported_binary_hash")
	}
	expected := "original"
	if action == "restore" {
		expected = "patched"
	}
	if state != expected {
		return nil, errors.New("patch_state_mismatch")
	}
	token := rand.Text()
	expires := time.Now().Add(5 * time.Minute)
	m.mu.Lock()
	defer m.mu.Unlock()
	for key, value := range m.plans {
		if time.Now().After(value.Expires) {
			delete(m.plans, key)
		}
	}
	if len(m.plans) >= 1024 {
		return nil, errors.New("patch_plan_limit")
	}
	m.plans[token] = plan{action, principal, digest(raw), expires, p}
	return map[string]any{"id": token, "action": action, "sha256": digest(raw), "version": p["version"], "module": p["module"], "expires": float64(expires.UnixMilli()) / 1000, "warning": "请先停止目标 daemon。此操作会修改二进制；QCS 模块还需配置本机网关和域名解析。"}, nil
}

// 按目录描述符打开各层，事务和备份不跟随替换后的路径或符号链接。
func directory(path string) (*os.File, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	for _, part := range strings.Split(strings.TrimPrefix(absolute, "/"), "/") {
		if part == "" {
			continue
		}
		next, e := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		unix.Close(fd)
		if e != nil {
			return nil, e
		}
		fd = next
	}
	return os.NewFile(uintptr(fd), absolute), nil
}
func at(dir *os.File, name string, flags int, mode uint32) (*os.File, error) {
	fd, err := unix.Openat(int(dir.Fd()), name, flags|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, mode)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), name), nil
}
func (m *Manager) transaction() (dir *os.File, release func(), err error) {
	base, e := directory(m.Root)
	if e != nil {
		return nil, nil, errors.New("patch_root_unavailable")
	}
	opened := []*os.File{base}
	cleanup := func() {
		for i := len(opened) - 1; i >= 0; i-- {
			opened[i].Close()
		}
	}
	defer func() {
		if err != nil {
			cleanup()
		}
	}()
	for _, name := range []string{"process-state", "patches"} {
		if e = unix.Mkdirat(int(base.Fd()), name, 0700); e != nil && !errors.Is(e, syscall.EEXIST) {
			return nil, nil, e
		}
	}
	state, e := at(base, "process-state", unix.O_RDONLY|unix.O_DIRECTORY, 0)
	if e != nil {
		return nil, nil, e
	}
	opened = append(opened, state)
	for _, name := range []string{"daemon.lock", "daemon-cn.lock"} {
		lock, e := at(state, name, unix.O_CREAT|unix.O_RDWR, 0600)
		if e != nil {
			return nil, nil, e
		}
		opened = append(opened, lock)
		if e = unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); e != nil {
			return nil, nil, errors.New("managed_process_busy")
		}
	}
	dir, e = at(base, "patches", unix.O_RDONLY|unix.O_DIRECTORY, 0)
	if e != nil {
		return nil, nil, e
	}
	opened = append(opened, dir)
	lock, e := at(dir, "transaction.lock", unix.O_CREAT|unix.O_RDWR, 0600)
	if e != nil {
		return nil, nil, e
	}
	opened = append(opened, lock)
	if e = unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); e != nil {
		return nil, nil, errors.New("patch_transaction_busy")
	}
	return dir, cleanup, nil
}
func (m *Manager) replace(ctx context.Context, raw []byte, expected string) error {
	if err := m.stopped(ctx, m.Binary); err != nil {
		return err
	}
	data, err := readFile(m.Binary, maximum)
	if err != nil || digest(data) != expected {
		return errors.New("binary_changed_since_plan")
	}
	dir, err := directory(filepath.Dir(m.Binary))
	if err != nil {
		return err
	}
	defer dir.Close()
	current, err := at(dir, filepath.Base(m.Binary), unix.O_RDONLY, 0)
	if err != nil {
		return err
	}
	info, err := current.Stat()
	current.Close()
	if err != nil {
		return err
	}
	name := ".qw-patch-" + rand.Text()
	tmp, err := at(dir, name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer unix.Unlinkat(int(dir.Fd()), name, 0)
	defer tmp.Close()
	if _, err = tmp.Write(raw); err != nil {
		return err
	}
	if err = tmp.Chmod(info.Mode().Perm()); err != nil {
		return err
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok && os.Geteuid() == 0 {
		if err = tmp.Chown(int(stat.Uid), int(stat.Gid)); err != nil {
			return err
		}
	}
	if err = tmp.Sync(); err != nil {
		return err
	}
	current, err = at(dir, filepath.Base(m.Binary), unix.O_RDONLY, 0)
	if err != nil {
		return errors.New("binary_changed_since_plan")
	}
	data, err = readOpen(current, maximum)
	current.Close()
	if err != nil || digest(data) != expected {
		return errors.New("binary_changed_since_plan")
	}
	if err = m.stopped(ctx, m.Binary); err != nil {
		return err
	}
	if err = unix.Renameat(int(dir.Fd()), name, int(dir.Fd()), filepath.Base(m.Binary)); err != nil {
		return err
	}
	return dir.Sync()
}
func (m *Manager) Execute(ctx context.Context, token, principal string) (map[string]any, error) {
	m.mu.Lock()
	p, ok := m.plans[token]
	if !ok || p.Principal != principal || time.Now().After(p.Expires) {
		m.mu.Unlock()
		return nil, errors.New("invalid_patch_plan")
	}
	delete(m.plans, token)
	m.mu.Unlock()
	dir, release, err := m.transaction()
	if err != nil {
		return nil, err
	}
	defer release()
	if err = m.stopped(ctx, m.Binary); err != nil {
		return nil, err
	}
	raw, profile, _, err := m.inspect()
	if err != nil {
		return nil, err
	}
	if digest(raw) != p.Hash || !reflect.DeepEqual(profile, p.Profile) {
		return nil, errors.New("binary_changed_since_plan")
	}
	backup := profile.text("original_sha256") + ".bin"
	var output []byte
	if p.Action == "apply" {
		if bytes.Count(raw, original) != 1 || bytes.Count(raw, replacement) != 0 {
			return nil, errors.New("patch_anchor_mismatch")
		}
		output = bytes.Replace(raw, original, replacement, 1)
		if len(raw) != len(output) || digest(output) != profile.text("patched_sha256") {
			return nil, errors.New("patch_output_hash_mismatch")
		}
		f, e := at(dir, backup, unix.O_RDONLY, 0)
		if errors.Is(e, syscall.ENOENT) {
			f, e = at(dir, backup, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL, 0600)
			if e != nil {
				return nil, e
			}
			_, e = f.Write(raw)
			if e == nil {
				e = f.Sync()
			}
			f.Close()
			if e != nil {
				return nil, e
			}
			if e = dir.Sync(); e != nil {
				return nil, e
			}
		} else if e != nil {
			return nil, errors.New("patch_backup_mismatch")
		} else {
			saved, e := readOpen(f, maximum)
			f.Close()
			if e != nil || digest(saved) != profile.text("original_sha256") {
				return nil, errors.New("patch_backup_mismatch")
			}
		}
	} else {
		f, e := at(dir, backup, unix.O_RDONLY, 0)
		if errors.Is(e, syscall.ENOENT) {
			candidates, _ := filepath.Glob(m.Binary + ".bak-p1-*")
			for _, candidate := range candidates {
				data, e := readFile(candidate, maximum)
				if e == nil && digest(data) == profile.text("original_sha256") {
					output = data
					break
				}
			}
			if output == nil {
				return nil, errors.New("patch_backup_missing")
			}
		} else if e != nil {
			return nil, errors.New("patch_backup_mismatch")
		} else {
			output, e = readOpen(f, maximum)
			f.Close()
			if e != nil {
				return nil, errors.New("patch_backup_mismatch")
			}
		}
		if digest(output) != profile.text("original_sha256") {
			return nil, errors.New("patch_backup_mismatch")
		}
	}
	if err = m.replace(ctx, output, p.Hash); err != nil {
		return nil, err
	}
	state := "patched"
	if p.Action == "restore" {
		state = "original"
	}
	return map[string]any{"ok": true, "state": state, "sha256": digest(output)}, nil
}
