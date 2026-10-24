package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"qoderwake-panel/internal/apierror"
	"qoderwake-panel/internal/managed"
	"qoderwake-panel/internal/platform"
)

type fileChecks struct {
	mu    sync.Mutex
	value map[string]any
}

func (s *Server) fileCheckStatus() map[string]any {
	s.fileChecks.mu.Lock()
	defer s.fileChecks.mu.Unlock()
	if s.fileChecks.value == nil {
		return map[string]any{"status": "idle"}
	}
	return s.fileChecks.value
}
func (s *Server) startFileCheck(b map[string]any) (any, error) {
	if str(b, "confirm") != "/api/file-check" {
		return nil, apierror.New(409, "confirmation_required")
	}
	finish, err := s.beginOperation()
	if err != nil {
		return nil, err
	}
	s.fileChecks.mu.Lock()
	defer s.fileChecks.mu.Unlock()
	if s.fileChecks.value["status"] == "running" {
		finish()
		return s.fileChecks.value, nil
	}
	s.fileChecks.value = map[string]any{"ok": true, "status": "running", "startedAt": float64(time.Now().UnixMilli()) / 1000}
	go func() {
		defer finish()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		files := s.checkFiles(ctx)
		status := "done"
		if ctx.Err() != nil {
			status = "failed"
		}
		result := map[string]any{"status": status, "checkedAt": float64(time.Now().UnixMilli()) / 1000, "files": files}
		s.fileChecks.mu.Lock()
		s.fileChecks.value = result
		s.fileChecks.mu.Unlock()
	}()
	return s.fileChecks.value, nil
}
func hashFile(ctx context.Context, path string, recorded *managed.File) map[string]any {
	out := map[string]any{"path": path, "present": false, "sameFile": nil, "sha256": nil, "recorded": false, "matchesRecorded": nil}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return out
	}
	defer f.Close()
	before, err := f.Stat()
	if err != nil || !before.Mode().IsRegular() || before.Size() > 1024*1024*1024 {
		return out
	}
	st, ok := before.Sys().(*syscall.Stat_t)
	if !ok {
		return out
	}
	out["present"] = true
	if recorded != nil {
		out["sameFile"] = uint64(st.Dev) == recorded.Device && uint64(st.Ino) == recorded.Inode
	}
	hash := sha256.New()
	buffer := make([]byte, 1024*1024)
	var total int64
	for {
		if ctx.Err() != nil {
			return out
		}
		n, e := f.Read(buffer)
		total += int64(n)
		if total > 1024*1024*1024 {
			return out
		}
		if n > 0 {
			hash.Write(buffer[:n])
		}
		if e == io.EOF {
			break
		}
		if e != nil {
			return out
		}
	}
	after, err := f.Stat()
	pathInfo, pathErr := os.Lstat(path)
	if err != nil || pathErr != nil || !os.SameFile(before, after) || !os.SameFile(before, pathInfo) || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		out["changedDuringCheck"] = true
		return out
	}
	digest := hex.EncodeToString(hash.Sum(nil))
	out["sha256"] = digest
	if recorded != nil && recorded.SHA256 != "" && recorded.SHA256 != strings.Repeat("0", 64) {
		out["recorded"] = true
		out["matchesRecorded"] = digest == recorded.SHA256
	}
	return out
}
func (s *Server) checkFiles(ctx context.Context) []map[string]any {
	files := []map[string]any{}
	if process, err := platform.Observe(ctx, os.Getpid()); err == nil {
		row := hashFile(ctx, process.Executable.Path, &managed.File{File: process.Executable})
		row["name"], row["kind"] = "panel", "executable"
		files = append(files, row)
	} else {
		files = append(files, map[string]any{"name": "panel", "status": "unavailable"})
	}
	found := false
	for _, name := range []string{"daemon", "daemon-cn"} {
		state, err := managed.Read(filepath.Join(s.Config.Root, "process-state", name+".json"))
		if err != nil {
			files = append(files, map[string]any{"name": name, "status": "invalid_record"})
			continue
		}
		if state == nil {
			continue
		}
		if state.Home != canonical(s.Config.Home) || state.Launch.Path != canonical(s.Config.CLI) {
			continue
		}
		found = true
		for kind, item := range map[string]*managed.File{"launch": &state.Launch, "script": state.Script} {
			if item == nil {
				continue
			}
			row := hashFile(ctx, item.Path, item)
			row["name"], row["kind"] = name, kind
			files = append(files, row)
		}
	}
	if !found {
		row := hashFile(ctx, s.Config.CLI, nil)
		row["name"], row["kind"], row["status"] = "daemon", "launch", "unmanaged"
		files = append(files, row)
	}
	return files
}
