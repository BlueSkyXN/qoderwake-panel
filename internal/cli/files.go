package cli

import (
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"unicode/utf8"

	"qoderwake-panel/internal/apierror"
	"qoderwake-panel/internal/auth"
)

const MaxFile = 64 * 1024 * 1024

var fileIDRE = regexp.MustCompile(`^[0-9a-f]{32}$`)
var base64RE = regexp.MustCompile(`^[A-Za-z0-9+/]*={0,2}$`)

type Files struct {
	root string
	mu   sync.Mutex
}
type File struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Reference string `json:"reference"`
	Output    bool   `json:"output"`
	Ready     bool   `json:"ready"`
}

func OpenFiles(root string) (*Files, error) {
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	return &Files{root: root}, nil
}
func filename(name string) bool {
	if name == "." || name == ".." || utf8.RuneCountInString(name) < 1 || utf8.RuneCountInString(name) > 150 || strings.ContainsAny(name, "/\\") {
		return false
	}
	for _, c := range name {
		if c < 32 {
			return false
		}
	}
	return true
}
func (f *Files) directory(identity auth.Identity) (string, error) {
	if identity.Role != "admin" {
		return "", apierror.New(403, "permission_denied")
	}
	dir := filepath.Join(f.root, auth.Digest(identity.Principal))
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	st, err := os.Lstat(dir)
	if err != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
		return "", apierror.New(403, "cli_file_rejected")
	}
	return dir, nil
}
func (f *Files) Create(identity auth.Identity, name string, output bool) (File, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !filename(name) {
		return File{}, apierror.Invalid("invalid_cli_filename")
	}
	dir, err := f.directory(identity)
	if err != nil {
		return File{}, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return File{}, err
	}
	if len(entries) >= 100 {
		return File{}, apierror.New(409, "cli_file_slots_full")
	}
	random, err := auth.Random(16)
	if err != nil {
		return File{}, err
	}
	bytes, err := base64.RawURLEncoding.DecodeString(random)
	if err != nil {
		return File{}, err
	}
	id := hex.EncodeToString(bytes)
	folder := filepath.Join(dir, id)
	if err = os.Mkdir(folder, 0700); err != nil {
		return File{}, err
	}
	if err = os.Mkdir(filepath.Join(folder, "files"), 0700); err != nil {
		return File{}, err
	}
	if !output {
		file, e := os.OpenFile(filepath.Join(folder, "files", name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if e != nil {
			return File{}, e
		}
		if e = file.Close(); e != nil {
			return File{}, e
		}
	}
	kind := "input"
	if output {
		kind = "output"
	}
	if err = os.WriteFile(filepath.Join(folder, "name"), []byte(name), 0600); err != nil {
		return File{}, err
	}
	if err = os.WriteFile(filepath.Join(folder, "type"), []byte(kind), 0600); err != nil {
		return File{}, err
	}
	return File{id, name, "@panel-file:" + id, output, !output}, nil
}
func readMetadata(path string) ([]byte, error) {
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	st, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() || st.Size() > 1024 {
		return nil, apierror.Invalid("invalid_cli_file")
	}
	return io.ReadAll(io.LimitReader(file, 1025))
}
func (f *Files) Path(identity auth.Identity, id string) (string, error) {
	if !fileIDRE.MatchString(id) {
		return "", apierror.Invalid("invalid_cli_file")
	}
	dir, err := f.directory(identity)
	if err != nil {
		return "", err
	}
	folder := filepath.Join(dir, id)
	for _, path := range []string{folder, filepath.Join(folder, "files")} {
		st, e := os.Lstat(path)
		if errors.Is(e, os.ErrNotExist) {
			return "", apierror.New(404, "cli_file_not_found")
		}
		if e != nil {
			return "", e
		}
		if !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
			return "", apierror.New(403, "cli_file_rejected")
		}
	}
	name, err := readMetadata(filepath.Join(folder, "name"))
	if err != nil {
		return "", apierror.New(404, "cli_file_not_found")
	}
	if !filename(string(name)) {
		return "", apierror.Invalid("invalid_cli_file")
	}
	path := filepath.Join(folder, "files", string(name))
	if st, e := os.Lstat(path); e == nil && (!st.Mode().IsRegular() || st.Mode()&os.ModeSymlink != 0) {
		return "", apierror.New(403, "cli_file_rejected")
	}
	return path, nil
}
func (f *Files) List(identity auth.Identity) ([]File, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	dir, err := f.directory(identity)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	out := []File{}
	for _, entry := range entries {
		if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		path, e := f.Path(identity, entry.Name())
		if e != nil {
			continue
		}
		kind, e := readMetadata(filepath.Join(dir, entry.Name(), "type"))
		if e != nil {
			continue
		}
		info, e := os.Stat(path)
		ready := e == nil && info.Mode().IsRegular()
		out = append(out, File{entry.Name(), filepath.Base(path), "@panel-file:" + entry.Name(), string(kind) == "output", ready})
	}
	return out, nil
}
func (f *Files) Append(identity auth.Identity, id string, offset int64, encoded string) (int64, error) {
	if offset < 0 || len(encoded) > 48000 || !base64RE.MatchString(encoded) {
		return 0, apierror.Invalid("invalid_cli_upload")
	}
	data, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil {
		return 0, apierror.Invalid("invalid_cli_upload")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	path, err := f.Path(identity, id)
	if err != nil {
		return 0, err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return 0, err
	}
	defer file.Close()
	st, err := file.Stat()
	if err != nil {
		return 0, err
	}
	if !st.Mode().IsRegular() || st.Size() != offset {
		return 0, apierror.New(409, "cli_upload_offset_changed")
	}
	if offset+int64(len(data)) > MaxFile {
		return 0, apierror.New(413, "cli_file_too_large")
	}
	n, err := file.Write(data)
	return offset + int64(n), err
}
func (f *Files) Read(identity auth.Identity, id string) ([]byte, string, error) {
	path, err := f.Path(identity, id)
	if err != nil {
		return nil, "", err
	}
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if errors.Is(err, os.ErrNotExist) {
		return nil, "", apierror.New(404, "cli_file_not_ready")
	}
	if err != nil {
		return nil, "", err
	}
	defer file.Close()
	st, err := file.Stat()
	if err != nil {
		return nil, "", err
	}
	if !st.Mode().IsRegular() || st.Size() > MaxFile {
		return nil, "", apierror.New(413, "cli_file_too_large")
	}
	data, err := io.ReadAll(io.LimitReader(file, MaxFile+1))
	if len(data) > MaxFile {
		return nil, "", apierror.New(413, "cli_file_too_large")
	}
	return data, filepath.Base(path), err
}
func (f *Files) Delete(identity auth.Identity, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	path, err := f.Path(identity, id)
	if err != nil {
		return err
	}
	if err = os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	folder := filepath.Dir(filepath.Dir(path))
	for _, p := range []string{filepath.Dir(path), filepath.Join(folder, "name"), filepath.Join(folder, "type"), folder} {
		if err = os.Remove(p); err != nil {
			return err
		}
	}
	return nil
}
func (f *Files) resolve(identity auth.Identity, value any) (any, error) {
	switch v := value.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, item := range v {
			next, err := f.resolve(identity, item)
			if err != nil {
				return nil, err
			}
			out[k] = next
		}
		return out, nil
	case []any:
		out := []any{}
		for _, item := range v {
			next, err := f.resolve(identity, item)
			if err != nil {
				return nil, err
			}
			out = append(out, next)
		}
		return out, nil
	case string:
		if strings.HasPrefix(v, "@panel-file:") {
			return f.Path(identity, strings.TrimPrefix(v, "@panel-file:"))
		}
	}
	return value, nil
}
func (f *Files) Resolve(identity auth.Identity, params Parameters) (Parameters, error) {
	args, err := f.resolve(identity, params.Arguments)
	if err != nil {
		return Parameters{}, err
	}
	opts, err := f.resolve(identity, params.Options)
	if err != nil {
		return Parameters{}, err
	}
	return Parameters{Arguments: args.(map[string]any), Options: opts.(map[string]any)}, nil
}
