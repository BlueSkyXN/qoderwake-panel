package gateway

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
	"qoderwake-panel/internal/platform"
)

type Manager struct {
	Root, Binary, ConfigPath, Upstream string
	Port                               int
	UID, GID                           uint32
}

func NewManager(root, binary, config, upstream string, port int, uid, gid uint32) (*Manager, error) {
	if port < 1 || port > 65535 || !ApprovedUpstream(upstream) {
		return nil, errors.New("invalid_gateway_configuration")
	}
	var err error
	root, err = filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	rawRoot := root
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return nil, err
	}
	if config == "" {
		config = filepath.Join(root, "config", "uplink-gw.json")
	}
	config, err = filepath.Abs(config)
	if err == nil && filepath.Dir(config) == filepath.Join(rawRoot, "config") {
		config = filepath.Join(root, "config", filepath.Base(config))
	}
	if err != nil || filepath.Dir(config) != filepath.Join(root, "config") {
		return nil, errors.New("invalid_gateway_config_path")
	}
	binary, err = filepath.Abs(binary)
	if err != nil {
		return nil, err
	}
	binary, err = filepath.EvalSymlinks(binary)
	if err != nil {
		return nil, err
	}
	if runtime.GOOS == "darwin" && uid != uint32(os.Geteuid()) {
		return nil, errors.New("gateway_user_unavailable")
	}
	return &Manager{root, binary, config, upstream, port, uid, gid}, nil
}
func token(bytes int) string {
	raw := make([]byte, bytes)
	if _, err := rand.Read(raw); err != nil {
		panic(err)
	}
	return hex.EncodeToString(raw)
}
func (m *Manager) store() string { return filepath.Join(m.Root, "gateway-runtime") }
func (m *Manager) label() string {
	sum := sha256.Sum256([]byte(m.Root + "\x00" + strconv.Itoa(m.Port)))
	return "com.qoderwake.panel.gateway-" + hex.EncodeToString(sum[:8])
}
func (m *Manager) plistPath() string { return filepath.Join(m.store(), "gateway.plist") }
func (m *Manager) locked() (func(), error) {
	if err := os.Mkdir(m.store(), 0750); err != nil && !os.IsExist(err) {
		return nil, err
	}
	dir, err := openDirectory(m.store())
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	if os.Geteuid() == 0 {
		if err = dir.Chown(0, int(m.GID)); err != nil {
			return nil, err
		}
	}
	if err = dir.Chmod(0750); err != nil {
		return nil, err
	}
	lock, err := openAt(dir, "manager.lock", unix.O_RDWR|unix.O_CREAT, 0600)
	if err != nil {
		return nil, err
	}
	if _, err = logInfo(lock, true); err != nil {
		lock.Close()
		return nil, err
	}
	if err = unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		lock.Close()
		return nil, errors.New("gateway_manager_busy")
	}
	return func() { lock.Close() }, nil
}
func writeAtomic(path string, raw []byte, mode os.FileMode, gid uint32) error {
	dir, err := openDirectory(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	var stat unix.Stat_t
	err = unix.Fstatat(int(dir.Fd()), filepath.Base(path), &stat, unix.AT_SYMLINK_NOFOLLOW)
	if err != nil && !errors.Is(err, syscall.ENOENT) {
		return err
	}
	if err == nil && stat.Mode&unix.S_IFMT != unix.S_IFREG {
		return errors.New("symlink_rejected")
	}
	name := ".replace-" + token(12)
	f, err := openAt(dir, name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	defer unix.Unlinkat(int(dir.Fd()), name, 0)
	if _, err = f.Write(raw); err != nil {
		return err
	}
	if err = f.Chmod(mode); err != nil {
		return err
	}
	if os.Geteuid() == 0 {
		if err = f.Chown(0, int(gid)); err != nil {
			return err
		}
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = unix.Renameat(int(dir.Fd()), name, int(dir.Fd()), filepath.Base(path)); err != nil {
		return err
	}
	return dir.Sync()
}
func (m *Manager) writeJSON(name string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return writeAtomic(filepath.Join(m.store(), name), raw, 0600, uint32(os.Getegid()))
}
func (m *Manager) clearJournal() error {
	dir, err := openDirectory(m.store())
	if err != nil {
		return err
	}
	defer dir.Close()
	if err = unix.Unlinkat(int(dir.Fd()), "operation.json", 0); err != nil && !errors.Is(err, syscall.ENOENT) {
		return err
	}
	return dir.Sync()
}
func (m *Manager) prepareLogs() error {
	parent := filepath.Join(m.Root, "logs")
	if err := os.MkdirAll(parent, 0750); err != nil {
		return err
	}
	dir, err := openDirectory(parent)
	if err != nil {
		return err
	}
	defer dir.Close()
	if os.Geteuid() == 0 {
		if err = dir.Chown(0, int(m.GID)); err != nil {
			return err
		}
	}
	if err = unix.Mkdirat(int(dir.Fd()), "gateway", 0700); err != nil && !errors.Is(err, syscall.EEXIST) {
		return err
	}
	audit, err := openAt(dir, "gateway", unix.O_RDONLY|unix.O_DIRECTORY, 0)
	if err != nil {
		return err
	}
	defer audit.Close()
	if os.Geteuid() == 0 {
		if err = audit.Chown(int(m.UID), int(m.GID)); err != nil {
			return err
		}
	}
	if err = audit.Chmod(0700); err != nil {
		return err
	}
	for _, name := range []string{"gateway.stdout.log", "gateway.stderr.log"} {
		f, e := openAt(dir, name, unix.O_WRONLY|unix.O_CREAT|unix.O_APPEND, 0600)
		if e != nil {
			return e
		}
		_, e = logInfo(f, false)
		if e == nil && os.Geteuid() == 0 {
			e = f.Chown(int(m.UID), int(m.GID))
		}
		f.Close()
		if e != nil {
			return e
		}
	}
	return nil
}
func (m *Manager) snapshot(p Policy) (Generation, error) {
	if err := p.Validate(); err != nil {
		return Generation{}, err
	}
	entries, err := os.ReadDir(m.store())
	if err != nil {
		return Generation{}, err
	}
	count := 0
	for _, entry := range entries {
		if generationRE.MatchString(entry.Name()) {
			count++
		}
	}
	if count >= 64 {
		return Generation{}, errors.New("gateway_generation_limit")
	}
	raw, err := regular(m.Binary, 512*1024*1024)
	if err != nil {
		return Generation{}, err
	}
	sum := sha256.Sum256(raw)
	path := filepath.Join(m.store(), "generation-"+token(12))
	if err = os.Mkdir(path, 0750); err != nil {
		return Generation{}, err
	}
	if os.Geteuid() == 0 {
		if err = os.Chown(path, 0, int(m.GID)); err != nil {
			return Generation{}, err
		}
	}
	gen := Generation{path, hex.EncodeToString(sum[:]), p.Hash(), p.Mode, m.Upstream, m.Port, m.Root, m.UID, m.GID}
	if err = writeAtomic(filepath.Join(path, "gateway"), raw, 0750, m.GID); err != nil {
		return gen, err
	}
	config, _ := json.Marshal(p)
	if err = writeAtomic(filepath.Join(path, "config.json"), config, 0640, m.GID); err != nil {
		return gen, err
	}
	manifest, _ := json.Marshal(generationManifest{ProtocolVersion, gen})
	if err = writeAtomic(filepath.Join(path, "manifest.json"), manifest, 0600, uint32(os.Getegid())); err != nil {
		return gen, err
	}
	return gen, nil
}
func (m *Manager) preflight(ctx context.Context, g Generation) error {
	if err := m.verifyGeneration(g); err != nil {
		return err
	}
	if err := m.prepareLogs(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, filepath.Join(g.Path, "gateway"), "gateway", "serve", "--check")
	cmd.Env = environmentList(m.environment(g, token(16)))
	cmd.Dir = g.Path
	if err := m.commandCredentials(cmd); err != nil {
		return err
	}
	output := &limitedBuffer{max: 65536}
	cmd.Stdout = output
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		return errors.New("gateway_preflight_failed_check_user_permissions")
	}
	var got struct {
		OK         bool   `json:"ok"`
		Schema     int    `json:"schemaVersion"`
		BinaryHash string `json:"binaryHash"`
		ConfigHash string `json:"configHash"`
		Mode       string `json:"mode"`
		Port       int    `json:"port"`
		Root       string `json:"root"`
		Upstream   string `json:"upstream"`
	}
	if decodeStrict(output.Bytes(), &got) != nil || !got.OK || got.Schema != ProtocolVersion || got.BinaryHash != g.BinaryHash || got.ConfigHash != g.ConfigHash || got.Mode != g.Mode || got.Port != g.Port || got.Root != g.Root || got.Upstream != g.Upstream {
		return errors.New("gateway_preflight_hash_mismatch")
	}
	return nil
}

type limitedBuffer struct {
	bytes.Buffer
	max int
}

func (b *limitedBuffer) Write(raw []byte) (int, error) {
	if b.Len()+len(raw) > b.max {
		return 0, errors.New("gateway_output_too_large")
	}
	return b.Buffer.Write(raw)
}
func environmentList(values map[string]string) []string {
	out := []string{}
	for key, value := range values {
		out = append(out, key+"="+value)
	}
	return out
}
func (m *Manager) capture(ctx context.Context, pid int, g Generation, nonce string, supervisor *Supervisor, previous *Generation) (*State, error) {
	p, err := platform.Observe(ctx, pid)
	if err != nil {
		return nil, err
	}
	env := m.environment(g, nonce)
	for key, value := range env {
		if p.Environment[key] != value {
			return nil, errors.New("gateway_process_identity_unknown")
		}
	}
	p.Environment = env
	s := &State{ProtocolVersion, g, p, nonce, runtime.GOOS, supervisor, previous}
	if err = m.validate(s); err != nil {
		return nil, err
	}
	return s, nil
}
func (m *Manager) healthy(ctx context.Context, s *State) bool {
	if m.verifyGeneration(s.Generation) != nil || m.identity(ctx, s) != "match" || platform.PortStatus(ctx, s.Process.PID, m.Port) != "owned" || m.supervision(ctx, s) != nil {
		return false
	}
	client := &http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer client.CloseIdleConnections()
	request, err := http.NewRequestWithContext(ctx, "GET", "http://127.0.0.1:"+strconv.Itoa(m.Port)+HealthPath, nil)
	if err != nil {
		return false
	}
	response, err := client.Do(request)
	if err != nil {
		return false
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 65537))
	if err != nil || len(raw) > 65536 || response.StatusCode != 200 {
		return false
	}
	var got map[string]any
	if json.Unmarshal(raw, &got) != nil {
		return false
	}
	expected := map[string]any{"schemaVersion": float64(ProtocolVersion), "service": "qw-control-gateway", "pid": float64(s.Process.PID), "nonce": s.Nonce, "binaryHash": s.Generation.BinaryHash, "configHash": s.Generation.ConfigHash, "mode": s.Generation.Mode, "port": float64(m.Port), "root": m.Root, "upstream": s.Generation.Upstream, "configPath": filepath.Join(s.Generation.Path, "config.json")}
	return reflect.DeepEqual(got, expected) && m.identity(ctx, s) == "match" && platform.PortStatus(ctx, s.Process.PID, m.Port) == "owned"
}
func (m *Manager) portFree(ctx context.Context) bool {
	owners, err := platform.Listeners(ctx, m.Port)
	return err == nil && len(owners) == 0
}
func (m *Manager) start(ctx context.Context, g Generation, nonce string, previous *Generation) (*State, error) {
	if !m.portFree(ctx) {
		return nil, errors.New("unmanaged_gateway_running_migration_required")
	}
	if err := m.verifyGeneration(g); err != nil {
		return nil, err
	}
	if err := m.prepareLogs(); err != nil {
		return nil, err
	}
	pid, supervisor, rollback, err := m.spawn(ctx, g, nonce)
	if err != nil {
		return nil, err
	}
	accepted := false
	defer func() {
		if !accepted && rollback != nil {
			_ = rollback()
		}
	}()
	deadline := time.NewTimer(15 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		s, err := m.capture(ctx, pid, g, nonce, supervisor, previous)
		if err == nil && m.healthy(ctx, s) {
			accepted = true
			return s, nil
		}
		if errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
			return nil, errors.New("gateway_start_failed")
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-deadline.C:
			return nil, errors.New("gateway_health_timeout")
		case <-ticker.C:
		}
	}
}
func (m *Manager) stop(ctx context.Context, s *State) error {
	identity := m.identity(ctx, s)
	if identity == "exited" {
		if !m.portFree(ctx) {
			return errors.New("gateway_listener_identity_unknown")
		}
		return m.cleanupSupervisor(ctx, s)
	}
	if identity != "match" {
		return errors.New("gateway_process_identity_unknown")
	}
	check := func() error {
		if m.identity(ctx, s) != "match" {
			return errors.New("gateway_process_identity_unknown")
		}
		status := platform.PortStatus(ctx, s.Process.PID, m.Port)
		if status != "owned" && status != "unbound" {
			return errors.New("gateway_listener_identity_unknown")
		}
		return nil
	}
	if err := check(); err != nil {
		return err
	}
	if err := m.terminate(ctx, s, check); err != nil {
		return err
	}
	deadline := time.NewTimer(15 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		if m.identity(ctx, s) == "exited" {
			if !m.portFree(ctx) {
				return errors.New("gateway_listener_identity_unknown")
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return errors.New("gateway_stop_timeout")
		case <-ticker.C:
		}
	}
}
func (m *Manager) publish(ctx context.Context, s *State) error {
	if !m.healthy(ctx, s) {
		return errors.New("gateway_health_invalid")
	}
	config, err := regular(filepath.Join(s.Generation.Path, "config.json"), 1024*1024)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(m.ConfigPath), 0700); err != nil {
		return err
	}
	if err = m.writeJSON("current.json", s); err != nil {
		return errors.New("gateway_publish_commit_unknown")
	}
	if err = writeAtomic(m.ConfigPath, config, 0600, uint32(os.Getegid())); err != nil {
		return errors.New("gateway_publish_commit_unknown")
	}
	return nil
}
func (m *Manager) removeGeneration(g Generation) error {
	if g.Path == "" {
		return nil
	}
	if m.generationValid(g) != nil {
		return errors.New("invalid_gateway_generation")
	}
	state, err := m.readState()
	if err != nil {
		return err
	}
	j, err := m.readJournal()
	if err != nil {
		return err
	}
	if state != nil && (state.Generation.Path == g.Path || state.Previous != nil && state.Previous.Path == g.Path) || j != nil && (j.Candidate.Path == g.Path || j.Previous != nil && (j.Previous.Generation.Path == g.Path || j.Previous.Previous != nil && j.Previous.Previous.Path == g.Path)) {
		return errors.New("gateway_generation_referenced")
	}
	dir, err := openDirectory(g.Path)
	if err != nil {
		return err
	}
	defer dir.Close()
	files, err := dir.ReadDir(-1)
	if err != nil {
		return err
	}
	for _, file := range files {
		if file.Name() != "gateway" && file.Name() != "config.json" && file.Name() != "manifest.json" {
			return errors.New("gateway_generation_contains_unknown_files")
		}
	}
	if err = m.verifyGeneration(g); err != nil {
		return err
	}
	discovery, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	processes, err := platform.ExecutableProcesses(discovery, filepath.Join(g.Path, "gateway"))
	if err != nil || len(processes) > 0 {
		return errors.New("gateway_generation_in_use")
	}
	for _, file := range files {
		if err = unix.Unlinkat(int(dir.Fd()), file.Name(), 0); err != nil {
			return err
		}
	}
	return os.Remove(g.Path)
}
