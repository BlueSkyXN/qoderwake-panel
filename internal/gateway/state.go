package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"syscall"

	"qoderwake-panel/internal/platform"
)

var generationRE = regexp.MustCompile(`^generation-[0-9a-f]{24}$`)
var hashRE = regexp.MustCompile(`^[0-9a-f]{64}$`)
var nonceRE = regexp.MustCompile(`^[0-9a-f]{32}$`)

type Generation struct {
	Path       string `json:"path"`
	BinaryHash string `json:"binaryHash"`
	ConfigHash string `json:"configHash"`
	Mode       string `json:"mode"`
	Upstream   string `json:"upstream"`
	Port       int    `json:"port"`
	Root       string `json:"root"`
	UID        uint32 `json:"uid"`
	GID        uint32 `json:"gid"`
}
type Supervisor struct {
	Path  string `json:"path"`
	Label string `json:"label"`
	Hash  string `json:"sha256"`
}
type State struct {
	SchemaVersion int              `json:"schemaVersion"`
	Generation    Generation       `json:"generation"`
	Process       platform.Process `json:"process"`
	Nonce         string           `json:"nonce"`
	Platform      string           `json:"platform"`
	Supervisor    *Supervisor      `json:"supervisor,omitempty"`
	Previous      *Generation      `json:"previous"`
}
type Journal struct {
	SchemaVersion int        `json:"schemaVersion"`
	OperationID   string     `json:"operationId"`
	Action        string     `json:"action"`
	Phase         string     `json:"phase"`
	Candidate     Generation `json:"candidate"`
	Previous      *State     `json:"previous"`
	Started       *State     `json:"started"`
	Nonce         string     `json:"nonce"`
}

func (m *Manager) generationValid(g Generation) error {
	if !filepath.IsAbs(g.Path) || filepath.Dir(g.Path) != m.store() || !generationRE.MatchString(filepath.Base(g.Path)) || !hashRE.MatchString(g.BinaryHash) || !hashRE.MatchString(g.ConfigHash) || g.Root != m.Root || g.Port != m.Port || g.UID != m.UID || g.GID != m.GID || !ApprovedUpstream(g.Upstream) || (g.Mode != "observe" && g.Mode != "enforce" && g.Mode != "strict") {
		return errors.New("invalid_gateway_generation")
	}
	info, err := os.Lstat(g.Path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("invalid_gateway_generation")
	}
	return nil
}
func (m *Manager) verifyGeneration(g Generation) error {
	if err := m.generationValid(g); err != nil {
		return err
	}
	hash, err := fileHash(filepath.Join(g.Path, "gateway"))
	if err != nil || hash != g.BinaryHash {
		return errors.New("gateway_binary_hash_mismatch")
	}
	policy, err := ReadPolicy(filepath.Join(g.Path, "config.json"))
	if err != nil || policy.Hash() != g.ConfigHash || policy.Mode != g.Mode {
		return errors.New("gateway_config_hash_mismatch")
	}
	return nil
}
func (m *Manager) command(g Generation) []string {
	return []string{filepath.Join(g.Path, "gateway"), "gateway", "serve"}
}
func (m *Manager) environment(g Generation, nonce string) map[string]string {
	return map[string]string{"QW_ROOT": m.Root, "QW_GW_PORT": strconv.Itoa(g.Port), "QW_GW_CONFIG": filepath.Join(g.Path, "config.json"), "QW_GW_CONFIG_HASH": g.ConfigHash, "QW_GW_BINARY_HASH": g.BinaryHash, "QW_GW_UPSTREAM": g.Upstream, "QW_GW_NONCE": nonce}
}
func (m *Manager) validate(s *State) error {
	if s == nil || s.SchemaVersion != ProtocolVersion || s.Platform != runtime.GOOS || m.generationValid(s.Generation) != nil || !nonceRE.MatchString(s.Nonce) || s.Process.PID < 1 || s.Process.StartTicks == 0 || s.Process.BootID == "" || s.Process.UID != m.UID || s.Process.Executable.Inode == 0 || s.Process.Executable.Path != filepath.Join(s.Generation.Path, "gateway") || !reflect.DeepEqual(s.Process.Cmdline, m.command(s.Generation)) || !reflect.DeepEqual(s.Process.Environment, m.environment(s.Generation, s.Nonce)) {
		return errors.New("invalid_gateway_state")
	}
	if s.Previous != nil && (m.generationValid(*s.Previous) != nil || s.Previous.Path == s.Generation.Path) {
		return errors.New("invalid_gateway_previous_generation")
	}
	if runtime.GOOS == "darwin" {
		if s.Supervisor == nil || s.Supervisor.Label != m.label() || s.Supervisor.Path != m.plistPath() || !hashRE.MatchString(s.Supervisor.Hash) {
			return errors.New("invalid_gateway_supervisor")
		}
	} else if s.Supervisor != nil {
		return errors.New("invalid_gateway_supervisor")
	}
	return nil
}
func (m *Manager) readState() (*State, error) {
	raw, err := regular(filepath.Join(m.store(), "current.json"), 1024*1024)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var version struct {
		Version int `json:"schemaVersion"`
	}
	if json.Unmarshal(raw, &version) != nil {
		return nil, errors.New("invalid_gateway_state")
	}
	if version.Version == 1 {
		return nil, errors.New("legacy_gateway_migration_required")
	}
	var state State
	if err := decodeStrict(raw, &state); err != nil {
		return nil, errors.New("invalid_gateway_state")
	}
	return &state, m.validate(&state)
}
func decodeStrict(raw []byte, value any) error {
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	if decoder.Decode(new(any)) != io.EOF {
		return errors.New("invalid_json")
	}
	return nil
}
func (m *Manager) identity(ctx context.Context, state *State) string {
	if m.validate(state) != nil {
		return "unknown"
	}
	p, err := platform.Observe(ctx, state.Process.PID)
	if err != nil {
		if errors.Is(syscall.Kill(state.Process.PID, 0), syscall.ESRCH) {
			return "exited"
		}
		return "unknown"
	}
	expected := state.Process
	if !platform.SameBirth(p, expected) || p.Executable != expected.Executable || !reflect.DeepEqual(p.Cmdline, expected.Cmdline) {
		return "unknown"
	}
	for key, value := range expected.Environment {
		if p.Environment[key] != value {
			return "unknown"
		}
	}
	return "match"
}
func (m *Manager) readJournal() (*Journal, error) {
	raw, err := regular(filepath.Join(m.store(), "operation.json"), 1024*1024)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var j Journal
	if decodeStrict(raw, &j) != nil || j.SchemaVersion != ProtocolVersion || !nonceRE.MatchString(j.OperationID) || !nonceRE.MatchString(j.Nonce) || m.generationValid(j.Candidate) != nil || (j.Action != "apply" && j.Action != "rollback") || (j.Phase != "prepared" && j.Phase != "starting" && j.Phase != "committing") {
		return nil, errors.New("invalid_gateway_journal")
	}
	if j.Previous != nil && m.validate(j.Previous) != nil {
		return nil, errors.New("invalid_gateway_journal")
	}
	if j.Started != nil && (m.validate(j.Started) != nil || j.Started.Nonce != j.Nonce || j.Started.Generation != j.Candidate) {
		return nil, errors.New("invalid_gateway_journal")
	}
	return &j, nil
}
