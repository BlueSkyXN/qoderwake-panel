// Package managed owns verified daemon lifecycle records for macOS and Linux.
package managed

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"unicode/utf8"

	"qoderwake-panel/internal/platform"
)

type File struct {
	platform.File
	SHA256 string `json:"sha256"`
}
type Supervisor struct {
	Kind   string `json:"kind"`
	Label  string `json:"label"`
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}
type State struct {
	SchemaVersion int               `json:"schemaVersion"`
	Name          string            `json:"name"`
	Profile       string            `json:"profile"`
	Status        string            `json:"status"`
	PID           int               `json:"pid"`
	StartTicks    uint64            `json:"startTicks"`
	BootID        string            `json:"bootId"`
	UID           uint32            `json:"uid"`
	Executable    platform.File     `json:"executable"`
	Launch        File              `json:"launch"`
	Script        *File             `json:"script"`
	Cmdline       []string          `json:"cmdline"`
	Home          string            `json:"home"`
	Port          int               `json:"port"`
	Mode          string            `json:"mode"`
	Endpoint      *string           `json:"endpoint"`
	HealthVersion *string           `json:"healthVersion"`
	Environment   map[string]string `json:"environment"`
	Platform      string            `json:"platform,omitempty"`
	Supervisor    *Supervisor       `json:"supervisor,omitempty"`
}

var safeName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
var bootID = regexp.MustCompile(`^[0-9a-f-]{16,64}$`)
var hashValue = regexp.MustCompile(`^[a-f0-9]{64}$`)
var labelValue = regexp.MustCompile(`^[A-Za-z0-9.-]{1,200}$`)
var ErrState = errors.New("invalid_process_state")
var DaemonKeys = fields("QODERWAKE_SESSION_PROJECTION_UPLINK QODERWAKE_REMOTE_EXECUTION_UPLINK QODER_SDK_CUSTOM_BASE_URL_BYOK QODERWAKE_HOT_DEPLOY QODER_MEMORY_DISABLE_EMBEDDING QODERWAKE_HOME QODER_ENV QODERWAKE_ENDPOINT_BASE_URL QODERWAKE_PRODUCT_BASE_URL QODERWAKE_CENTER_BASE_URL QODERWAKE_OPENAPI_BASE_URL QODERWAKE_CODEBASE_BASE_URL QODERWAKE_API_INVOKE_BASE_URL QODER_SERVER_ENDPOINT QODER_CENTER_ENDPOINT QODER_CONFIG_SERVICE_URL")
var PanelKeys = fields("QW_ROOT QW_HOME QW_DAEMON_BIN QW_DAEMON_URL QW_DAEMON_FRONTEND_TOKEN_FILE QW_WHITELIST QW_PORT QW_BIND QW_PUBLIC_ORIGIN QW_ALLOWED_CLIENTS QW_REQUIRE_TLS QW_TLS_CERT QW_TLS_KEY QW_HEALTH_CA_FILE QW_HEALTH_SERVER_NAME QW_GW_CONFIG QW_GW_PORT QW_GW_USER QW_GW_UPSTREAM QW_CN_PORT QW_PATCH_REGISTRY QW_EXPERIMENTAL_PLUGIN_WRITES QODERWAKE_HOT_DEPLOY QODER_MEMORY_DISABLE_EMBEDDING QODER_SDK_CUSTOM_BASE_URL_BYOK QODERWAKE_SESSION_PROJECTION_UPLINK QODERWAKE_REMOTE_EXECUTION_UPLINK")
var ProxyKeys = fields("HTTP_PROXY HTTPS_PROXY ALL_PROXY NO_PROXY http_proxy https_proxy all_proxy no_proxy NODE_EXTRA_CA_CERTS NODE_USE_SYSTEM_CA QODER_HTTPDNS QODER_SDK_CUSTOM_BASE_URL QODER_CLOUD_AGENT_API_BASE")
var GatewayKeys = fields("QODERWAKE_ENDPOINT_BASE_URL QODERWAKE_PRODUCT_BASE_URL QODERWAKE_CENTER_BASE_URL QODERWAKE_OPENAPI_BASE_URL QODERWAKE_CODEBASE_BASE_URL QODERWAKE_API_INVOKE_BASE_URL QODER_SERVER_ENDPOINT QODER_CENTER_ENDPOINT")

func fields(value string) map[string]bool {
	result := map[string]bool{}
	for _, v := range strings.Fields(value) {
		result[v] = true
	}
	return result
}
func validFile(f platform.File) bool { return filepath.IsAbs(f.Path) && f.Inode > 0 }
func (s *State) Validate() error {
	if s == nil || (s.SchemaVersion != 1 && s.SchemaVersion != 2) || !safeName.MatchString(s.Name) || (s.Profile != "daemon" && s.Profile != "panel") || (s.Status != "starting" && s.Status != "running") || s.PID < 1 || s.StartTicks == 0 || !bootID.MatchString(s.BootID) {
		return ErrState
	}
	if s.SchemaVersion == 1 && (s.Platform != "" || s.Supervisor != nil) {
		return ErrState
	}
	if s.SchemaVersion == 2 && s.Platform != "linux" && s.Platform != "darwin" {
		return ErrState
	}
	if !validFile(s.Executable) || !validFile(s.Launch.File) || !hashValue.MatchString(s.Launch.SHA256) || s.Launch.File != s.Executable {
		return ErrState
	}
	if s.Script != nil && (!validFile(s.Script.File) || !hashValue.MatchString(s.Script.SHA256)) {
		return ErrState
	}
	if len(s.Cmdline) == 0 || len(s.Cmdline) > 64 {
		return ErrState
	}
	for _, arg := range s.Cmdline {
		if arg == "" || len([]rune(arg)) > 4096 {
			return ErrState
		}
	}
	expected := []string{s.Executable.Path, "start", "--foreground"}
	if s.Profile == "panel" && s.Script != nil {
		expected = []string{s.Executable.Path, s.Script.Path}
	}
	commandMatch := reflect.DeepEqual(s.Cmdline, expected)
	if s.SchemaVersion == 2 && s.Profile == "daemon" {
		commandMatch = commandMatch || reflect.DeepEqual(s.Cmdline, daemonCommand(s.Executable.Path, s.Port))
	}
	if !commandMatch || !filepath.IsAbs(s.Home) || s.Port < 1 || s.Port > 65535 {
		return ErrState
	}
	if s.Mode != "panel" && s.Mode != "direct" && s.Mode != "gateway" {
		return ErrState
	}
	endpoint := ""
	if s.Endpoint != nil {
		endpoint = *s.Endpoint
		u, e := url.Parse(endpoint)
		if e != nil || len(endpoint) > 2048 || u.Scheme != "http" || !(u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost" || u.Hostname() == "::1") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return ErrState
		}
	}
	if (s.Mode == "gateway") != (endpoint != "") {
		return ErrState
	}
	if s.HealthVersion != nil && (len(*s.HealthVersion) == 0 || len([]rune(*s.HealthVersion)) > 128) {
		return ErrState
	}
	if s.Environment == nil {
		return ErrState
	}
	allowed := DaemonKeys
	if s.Profile == "panel" {
		allowed = PanelKeys
	}
	for key, value := range s.Environment {
		if (!allowed[key] && !(s.SchemaVersion == 2 && ProxyKeys[key])) || len([]rune(value)) > 4096 {
			return ErrState
		}
	}
	if s.Profile == "panel" {
		if s.Mode != "panel" || s.Script == nil || s.Environment["QW_ROOT"] == "" || s.Environment["QW_HOME"] != s.Home || s.Environment["QW_PORT"] != strconv.Itoa(s.Port) {
			return ErrState
		}
	} else {
		if (s.Mode != "direct" && s.Mode != "gateway") || s.Script != nil || s.Environment["QODERWAKE_HOME"] != s.Home {
			return ErrState
		}
		for key := range GatewayKeys {
			value, exists := s.Environment[key]
			if s.Mode == "gateway" && (!exists || value != endpoint) || s.Mode == "direct" && exists {
				return ErrState
			}
		}
	}
	if s.Supervisor != nil {
		v := s.Supervisor
		if s.SchemaVersion != 2 || s.Platform != "darwin" || v.Kind != "launchd" || !labelValue.MatchString(v.Label) || !filepath.IsAbs(v.Path) || !hashValue.MatchString(v.SHA256) {
			return ErrState
		}
	}
	return nil
}
func Read(path string) (*State, error) {
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > 1024*1024 {
		return nil, ErrState
	}
	raw, err := io.ReadAll(io.LimitReader(file, 1024*1024+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > 1024*1024 || !utf8.Valid(raw) {
		return nil, ErrState
	}
	var state State
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&state) != nil || decoder.Decode(new(any)) != io.EOF {
		return nil, ErrState
	}
	keys := "schemaVersion name profile status pid startTicks bootId uid executable launch script cmdline home port mode endpoint healthVersion environment"
	var root map[string]json.RawMessage
	if json.Unmarshal(raw, &root) != nil {
		return nil, ErrState
	}
	if state.SchemaVersion == 2 {
		keys += " platform"
		if _, exists := root["supervisor"]; exists {
			keys += " supervisor"
		}
	}
	if !exactKeys(raw, keys) || !exactKeys(root["executable"], "path device inode") || !exactKeys(root["launch"], "path device inode sha256") || state.Script != nil && !exactKeys(root["script"], "path device inode sha256") || state.Supervisor != nil && !exactKeys(root["supervisor"], "kind label path sha256") {
		return nil, ErrState
	}
	if err = state.Validate(); err != nil {
		return nil, err
	}
	return &state, nil
}
func exactKeys(raw []byte, keys string) bool {
	var value map[string]any
	if json.Unmarshal(raw, &value) != nil {
		return false
	}
	expected := strings.Fields(keys)
	if len(value) != len(expected) {
		return false
	}
	for _, key := range expected {
		if _, ok := value[key]; !ok {
			return false
		}
	}
	return true
}
func IdentityStatus(ctx context.Context, state *State) string {
	if state.Validate() != nil || state.Platform != "" && state.Platform != runtime.GOOS {
		return "unknown"
	}
	if !platform.FileMatches(state.Launch.File) || state.Script != nil && !platform.FileMatches(state.Script.File) {
		return "mismatch"
	}
	observed, err := platform.Observe(ctx, state.PID)
	if err != nil {
		if os.IsNotExist(err) && errors.Is(syscall.Kill(state.PID, 0), syscall.ESRCH) {
			return "exited"
		}
		return "unknown"
	}
	if observed.PID != state.PID || observed.StartTicks != state.StartTicks || observed.BootID != state.BootID || observed.UID != state.UID || observed.Executable != state.Executable || !reflect.DeepEqual(observed.Cmdline, state.Cmdline) {
		return "mismatch"
	}
	for key, value := range state.Environment {
		if actual, exists := observed.Environment[key]; !exists || actual != value {
			return "mismatch"
		}
	}
	return "match"
}
