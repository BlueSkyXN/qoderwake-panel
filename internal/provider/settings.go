// Package provider manages revision-checked settings and explicit connection probes.
package provider

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"syscall"
	"unicode/utf8"

	"qoderwake-panel/internal/apierror"
)

const MaxSettings = 4 * 1024 * 1024

var RevisionPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)
var NamePattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

type Snapshot struct {
	Raw      []byte
	Data     map[string]any
	Revision string
}
type Settings struct {
	Path, Recovery string
	mu             sync.Mutex
}

func NewSettings(path, recovery string) *Settings { return &Settings{Path: path, Recovery: recovery} }
func Revision(raw []byte) string                  { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }
func RequireRevision(value string) error {
	if !RevisionPattern.MatchString(value) {
		return apierror.Invalid("provider_revision_required")
	}
	return nil
}
func (s *Settings) Read() (Snapshot, error) {
	raw := []byte("{}")
	file, err := os.OpenFile(s.Path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return Snapshot{}, apierror.Invalid("provider_settings_unavailable")
	}
	if err == nil {
		defer file.Close()
		st, e := file.Stat()
		if e != nil || !st.Mode().IsRegular() {
			return Snapshot{}, apierror.Invalid("provider_settings_unavailable")
		}
		if st.Size() > MaxSettings {
			return Snapshot{}, apierror.Invalid("provider_settings_too_large")
		}
		raw, e = io.ReadAll(io.LimitReader(file, MaxSettings+1))
		if e != nil {
			return Snapshot{}, e
		}
		if len(raw) > MaxSettings {
			return Snapshot{}, apierror.Invalid("provider_settings_too_large")
		}
	}
	if !utf8.Valid(raw) {
		return Snapshot{}, apierror.Invalid("provider_settings_invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var data map[string]any
	if decoder.Decode(&data) != nil || data == nil || decoder.Decode(new(any)) != io.EOF {
		return Snapshot{}, apierror.Invalid("provider_settings_invalid")
	}
	if data["providers"] == nil {
		data["providers"] = map[string]any{}
	}
	if _, ok := data["providers"].(map[string]any); !ok {
		return Snapshot{}, apierror.Invalid("provider_settings_invalid")
	}
	return Snapshot{raw, data, Revision(raw)}, nil
}
func Editability(value any) (bool, string) {
	item, ok := value.(map[string]any)
	if !ok {
		return false, "provider_schema_unsupported"
	}
	if item["type"] != nil && item["type"] != "openai-compatible" {
		return false, "provider_protocol_unsupported"
	}
	if item["authType"] != nil && item["authType"] != "bearer" {
		return false, "provider_auth_unsupported"
	}
	models, ok := item["models"].([]any)
	if !ok || len(models) != 1 {
		return false, "provider_model_count_unsupported"
	}
	row, ok := models[0].(map[string]any)
	if !ok {
		return false, "provider_schema_unsupported"
	}
	model, ok := row["model"].(string)
	if !ok || strings.TrimSpace(model) == "" {
		return false, "provider_schema_unsupported"
	}
	return true, ""
}
func Models(name string, value any) []string {
	out := []string{}
	item, ok := value.(map[string]any)
	if !ok {
		return out
	}
	models, _ := item["models"].([]any)
	for _, raw := range models {
		row, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		model, ok := row["model"].(string)
		if ok && strings.TrimSpace(model) != "" {
			out = append(out, name+"/"+strings.TrimSpace(model))
		}
	}
	return out
}
func (s *Settings) Summaries() ([]map[string]any, string, error) {
	snapshot, err := s.Read()
	if err != nil {
		return nil, "", err
	}
	items := snapshot.Data["providers"].(map[string]any)
	names := make([]string, 0, len(items))
	for name := range items {
		names = append(names, name)
	}
	sort.Strings(names)
	out := []map[string]any{}
	for _, name := range names {
		value := items[name]
		editable, reason := Editability(value)
		item, _ := value.(map[string]any)
		models, display := []any{}, []any{}
		entries, _ := item["models"].([]any)
		for _, entry := range entries {
			row, ok := entry.(map[string]any)
			if ok {
				models = append(models, row["model"])
				display = append(display, row["displayName"])
			}
		}
		var readOnly any
		if reason != "" {
			readOnly = reason
		}
		key, _ := item["apiKey"].(string)
		out = append(out, map[string]any{"name": name, "baseUrl": item["baseUrl"], "type": item["type"], "authType": item["authType"], "models": models, "modelKeys": Models(name, item), "displayNames": display, "keyConfigured": key != "", "panelManaged": editable, "readOnlyReason": readOnly})
	}
	return out, snapshot.Revision, nil
}
func (s *Settings) LegacyBackups() map[string]any {
	matches, _ := filepath.Glob(filepath.Join(filepath.Dir(s.Path), "settings.panel-bak-*.json"))
	count := 0
	var size int64
	var oldest any
	var first int64
	for _, path := range matches {
		st, e := os.Lstat(path)
		if e != nil || !st.Mode().IsRegular() {
			continue
		}
		count++
		size += st.Size()
		if first == 0 || st.ModTime().UnixNano() < first {
			first = st.ModTime().UnixNano()
			oldest = float64(first) / 1e9
		}
	}
	return map[string]any{"count": count, "bytes": size, "oldest": oldest}
}
func atomicBytes(path string, raw []byte, beforeReplace func() error) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	if st, e := os.Lstat(path); e == nil && st.Mode()&os.ModeSymlink != 0 {
		return apierror.Invalid("symlink_not_allowed")
	}
	file, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+"-")
	if err != nil {
		return err
	}
	temporary := file.Name()
	defer os.Remove(temporary)
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
	if beforeReplace != nil {
		if err = beforeReplace(); err != nil {
			return err
		}
	}
	if err = os.Rename(temporary, path); err != nil {
		return err
	}
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
func (s *Settings) Update(expected string, transform func(map[string]any) error) (Snapshot, error) {
	if err := RequireRevision(expected); err != nil {
		return Snapshot{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(s.Path), 0700); err != nil {
		return Snapshot{}, err
	}
	lock, err := os.OpenFile(filepath.Join(filepath.Dir(s.Path), "."+filepath.Base(s.Path)+".panel.lock"), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return Snapshot{}, err
	}
	defer lock.Close()
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return Snapshot{}, err
	}
	before, err := s.Read()
	if err != nil {
		return Snapshot{}, err
	}
	if before.Revision != expected {
		return Snapshot{}, apierror.New(409, "version_conflict")
	}
	if err = transform(before.Data); err != nil {
		return Snapshot{}, err
	}
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err = encoder.Encode(before.Data); err != nil {
		return Snapshot{}, err
	}
	encoded := bytes.TrimSuffix(buffer.Bytes(), []byte("\n"))
	if len(encoded) > MaxSettings {
		return Snapshot{}, apierror.Invalid("provider_settings_too_large")
	}
	check := func() error {
		current, e := s.Read()
		if e != nil {
			return e
		}
		if current.Revision != expected {
			return apierror.New(409, "version_conflict")
		}
		return nil
	}
	if err = check(); err != nil {
		return Snapshot{}, err
	}
	if _, e := os.Stat(s.Path); e == nil {
		if err = atomicBytes(s.Recovery, before.Raw, nil); err != nil {
			return Snapshot{}, err
		}
	}
	if err = atomicBytes(s.Path, encoded, check); err != nil {
		return Snapshot{}, err
	}
	return Snapshot{encoded, before.Data, Revision(encoded)}, nil
}
func (s *Settings) Write(name, baseURL, key, model, display, expected string) (Snapshot, error) {
	name, model, key, display = strings.TrimSpace(name), strings.TrimSpace(model), strings.TrimSpace(key), strings.TrimSpace(display)
	if !NamePattern.MatchString(name) || len(name) > 120 {
		return Snapshot{}, apierror.Invalid("invalid_provider_name")
	}
	if model == "" || utf8.RuneCountInString(model) > 200 || utf8.RuneCountInString(display) > 200 || utf8.RuneCountInString(key) > 2048 {
		return Snapshot{}, apierror.Invalid("invalid_text")
	}
	var err error
	baseURL, err = ValidateURL(baseURL)
	if err != nil {
		return Snapshot{}, err
	}
	return s.Update(expected, func(data map[string]any) error {
		providers := data["providers"].(map[string]any)
		old := map[string]any{}
		if value, exists := providers[name]; exists {
			if editable, _ := Editability(value); !editable {
				return apierror.Invalid("provider_requires_official_editor")
			}
			old = value.(map[string]any)
		}
		saved := key
		if saved == "" {
			saved, _ = old["apiKey"].(string)
		}
		if saved == "" {
			return apierror.Invalid("api_key_required")
		}
		item := map[string]any{}
		if models, ok := old["models"].([]any); ok && len(models) > 0 {
			for k, v := range models[0].(map[string]any) {
				item[k] = v
			}
		}
		item["model"] = model
		item["displayName"] = display
		if display == "" {
			item["displayName"] = model
		}
		old["baseUrl"] = baseURL
		old["apiKey"] = saved
		old["type"] = "openai-compatible"
		old["authType"] = "bearer"
		old["model"] = model
		old["models"] = []any{item}
		providers[name] = old
		return nil
	})
}
func (s *Settings) Delete(name, expected string) (Snapshot, error) {
	return s.Update(expected, func(data map[string]any) error {
		providers := data["providers"].(map[string]any)
		value, exists := providers[name]
		if !exists {
			return apierror.Invalid("provider_not_found")
		}
		if editable, _ := Editability(value); !editable {
			return apierror.Invalid("provider_requires_official_editor")
		}
		delete(providers, name)
		return nil
	})
}
