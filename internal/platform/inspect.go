// Package platform verifies local process identity without relying on a Python runtime.
package platform

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"
)

type File struct {
	Path   string `json:"path"`
	Device uint64 `json:"device"`
	Inode  uint64 `json:"inode"`
}
type Process struct {
	PID         int               `json:"pid"`
	StartTicks  uint64            `json:"startTicks"`
	BootID      string            `json:"bootId"`
	UID         uint32            `json:"uid"`
	Executable  File              `json:"executable"`
	Cmdline     []string          `json:"cmdline"`
	Environment map[string]string `json:"environment"`
}

var ErrVisibility = errors.New("process_visibility_required")

func FileIdentity(path string) (File, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return File{}, err
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return File{}, err
	}
	info, err := os.Stat(canonical)
	if err != nil {
		return File{}, err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() {
		return File{}, ErrVisibility
	}
	return File{canonical, uint64(st.Dev), uint64(st.Ino)}, nil
}
func FileMatches(value File) bool {
	current, e := FileIdentity(value.Path)
	return e == nil && current == value
}
func read(path string, limit int64) ([]byte, error) {
	f, e := os.Open(path)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	data, e := io.ReadAll(io.LimitReader(f, limit+1))
	if e != nil {
		return nil, e
	}
	if int64(len(data)) > limit {
		return nil, ErrVisibility
	}
	return data, nil
}
func environment(raw []byte) map[string]string {
	values := map[string]string{}
	for _, item := range bytes.Split(raw, []byte{0}) {
		key, value, ok := bytes.Cut(item, []byte{'='})
		if !ok || !utf8.Valid(value) {
			continue
		}
		ascii := true
		for _, b := range key {
			ascii = ascii && b < 128
		}
		if ascii {
			values[string(key)] = string(value)
		}
	}
	return values
}

type bounded struct {
	bytes.Buffer
	max int
}

func (b *bounded) Write(p []byte) (int, error) {
	if b.Len()+len(p) > b.max {
		return 0, ErrVisibility
	}
	return b.Buffer.Write(p)
}
func command(ctx context.Context, name string, args ...string) ([]byte, int, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	out, stderr := &bounded{max: 4 * 1024 * 1024}, &bounded{max: 8192}
	cmd.Stdout = out
	cmd.Stderr = stderr
	err := cmd.Run()
	if err == nil {
		return out.Bytes(), 0, nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 && stderr.Len() == 0 && ctx.Err() == nil {
		return out.Bytes(), 1, nil
	}
	return nil, 0, ErrVisibility
}
func portOpen(port int) bool {
	for _, host := range []string{"127.0.0.1", "::1"} {
		connection, err := net.DialTimeout("tcp", net.JoinHostPort(host, strconv.Itoa(port)), 150*time.Millisecond)
		if err == nil {
			connection.Close()
			return true
		}
	}
	return false
}
func PortStatus(ctx context.Context, pid, port int) string {
	return portStatus(ctx, pid, port)
}
func listenerStatus(ctx context.Context, pid, port int) string {
	owners, err := Listeners(ctx, port)
	if err != nil {
		return "unknown"
	}
	if len(owners) == 0 {
		return "unbound"
	}
	if len(owners) == 1 && owners[pid] {
		return "owned"
	}
	return "other"
}
func SameBirth(a, b Process) bool {
	return a.PID == b.PID && a.StartTicks == b.StartTicks && a.BootID == b.BootID && a.UID == b.UID
}
func normalizedPath(path string) string {
	value, e := filepath.EvalSymlinks(path)
	if e == nil {
		return value
	}
	return strings.TrimSpace(path)
}
