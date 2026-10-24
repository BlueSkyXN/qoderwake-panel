package gateway

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
	"qoderwake-panel/internal/jsoncompat"
)

const logLimit = 8 * 1024 * 1024
const logBackups = 3
const maxRecord = 16384

// openDirectory 与旧网关相同：逐层打开已存在目录，拒绝符号链接替换。
func openDirectory(path string) (*os.File, error) {
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
func openAt(dir *os.File, name string, flags int, mode uint32) (*os.File, error) {
	fd, err := unix.Openat(int(dir.Fd()), name, flags|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, mode)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), name), nil
}
func logInfo(file *os.File, private bool) (os.FileInfo, error) {
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || stat.Nlink != 1 || (private && (stat.Uid != uint32(os.Geteuid()) || info.Mode().Perm()&0077 != 0)) {
		return nil, errors.New("unsafe_gateway_log")
	}
	return info, nil
}

type Logger struct {
	Path  string
	mu    sync.Mutex
	limit int64
}

func NewLogger(root string) *Logger {
	return &Logger{Path: filepath.Join(root, "logs", "gateway", "uplink-gw.jsonl"), limit: logLimit}
}
func (l *Logger) Prepare() error { return l.append(nil) }
func (l *Logger) Append(record Record) error {
	raw, err := json.Marshal(record)
	if err != nil {
		return err
	}
	return l.append(append(raw, '\n'))
}
func (l *Logger) append(raw []byte) error {
	if len(raw) > maxRecord {
		return errors.New("gateway_log_record_too_large")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	parent := filepath.Dir(l.Path)
	if err := os.MkdirAll(parent, 0700); err != nil {
		return err
	}
	dir, err := openDirectory(parent)
	if err != nil {
		return err
	}
	defer dir.Close()
	lock, err := openAt(dir, filepath.Base(l.Path)+".lock", unix.O_WRONLY|unix.O_CREAT, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if _, err = logInfo(lock, true); err != nil {
		return err
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		err = unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			break
		}
		if (!errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN)) || time.Now().After(deadline) {
			return errors.New("gateway_log_busy")
		}
		time.Sleep(10 * time.Millisecond)
	}
	names := []string{filepath.Base(l.Path)}
	for i := 1; i <= logBackups; i++ {
		names = append(names, filepath.Base(l.Path)+"."+strconv.Itoa(i))
	}
	entries := make([]os.FileInfo, len(names))
	for i, name := range names {
		f, e := openAt(dir, name, unix.O_RDONLY, 0)
		if os.IsNotExist(e) {
			continue
		}
		if e != nil {
			return e
		}
		entries[i], e = logInfo(f, true)
		f.Close()
		if e != nil {
			return e
		}
	}
	if current := entries[0]; len(raw) > 0 && current != nil && current.Size() > 0 && current.Size()+int64(len(raw)) > l.limit {
		if entries[len(entries)-1] != nil {
			if err = unix.Unlinkat(int(dir.Fd()), names[len(names)-1], 0); err != nil {
				return err
			}
		}
		for i := len(names) - 2; i >= 0; i-- {
			if entries[i] != nil {
				if err = unix.Renameat(int(dir.Fd()), names[i], int(dir.Fd()), names[i+1]); err != nil {
					return err
				}
			}
		}
		if err = dir.Sync(); err != nil {
			return err
		}
	}
	file, err := openAt(dir, names[0], unix.O_WRONLY|unix.O_CREAT|unix.O_APPEND, 0600)
	if err != nil {
		return err
	}
	defer file.Close()
	if _, err = logInfo(file, true); err != nil {
		return err
	}
	n, err := file.Write(raw)
	if err == nil && n != len(raw) {
		err = io.ErrShortWrite
	}
	return err
}

type Window struct {
	Lines     []string `json:"-"`
	BytesRead int64    `json:"bytesRead"`
	FileBytes int64    `json:"fileBytes"`
	Sources   int      `json:"sources"`
	Truncated bool     `json:"truncated"`
	MaxBytes  int64    `json:"maxBytes"`
	MaxLines  int      `json:"maxLines"`
	Available bool     `json:"available"`
}

func Tail(paths []string, maxLines int, maxBytes int64) (Window, error) {
	window := Window{Lines: []string{}, MaxLines: maxLines, MaxBytes: maxBytes, Available: true}
	if maxLines < 1 || maxBytes < 1 || maxLines > 100000 || maxBytes > 64*1024*1024 {
		return window, errors.New("invalid_log_window")
	}
	for _, path := range paths {
		if len(window.Lines) >= maxLines || window.BytesRead >= maxBytes {
			window.Truncated = true
			break
		}
		dir, err := openDirectory(filepath.Dir(path))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			window.Available = false
			return window, err
		}
		f, err := openAt(dir, filepath.Base(path), unix.O_RDONLY, 0)
		dir.Close()
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			window.Available = false
			return window, err
		}
		lines, err := tailFile(f, &window)
		f.Close()
		if err != nil {
			window.Available = false
			return window, err
		}
		window.Lines = append(lines, window.Lines...)
	}
	return window, nil
}
func tailFile(file *os.File, window *Window) ([]string, error) {
	info, err := logInfo(file, false)
	if err != nil {
		return nil, err
	}
	window.FileBytes += info.Size()
	window.Sources++
	position := info.Size()
	remaining := window.MaxLines - len(window.Lines)
	newlines := 0
	chunks := [][]byte{}
	for position > 0 && window.BytesRead < window.MaxBytes && newlines <= remaining {
		size := min(position, 16384, window.MaxBytes-window.BytesRead)
		position -= size
		raw := make([]byte, size)
		n, err := file.ReadAt(raw, position)
		window.BytesRead += int64(n)
		if err != nil || int64(n) != size {
			return nil, errors.New("log_changed_during_read")
		}
		chunks = append(chunks, raw)
		newlines += bytes.Count(raw, []byte{'\n'})
	}
	var raw []byte
	for i := len(chunks) - 1; i >= 0; i-- {
		raw = append(raw, chunks[i]...)
	}
	lines := bytes.Split(raw, []byte{'\n'})
	if position > 0 {
		lines = lines[1:]
		window.Truncated = true
	}
	if len(lines) > 0 {
		lines = lines[:len(lines)-1]
	}
	if len(lines) > remaining {
		window.Truncated = true
		lines = lines[len(lines)-remaining:]
	}
	result := make([]string, 0, len(lines))
	for _, line := range lines {
		result = append(result, jsoncompat.DecodeUTF8(line))
	}
	return result, nil
}
