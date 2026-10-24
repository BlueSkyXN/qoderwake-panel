//go:build linux

package platform

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func Observe(ctx context.Context, pid int) (Process, error) { return observeLinux(ctx, "/proc", pid) }
func observeLinux(_ context.Context, root string, pid int) (Process, error) {
	if pid < 1 {
		return Process{}, ErrVisibility
	}
	directory := filepath.Join(root, strconv.Itoa(pid))
	start := func() (uint64, error) {
		raw, e := read(filepath.Join(directory, "stat"), 16384)
		if e != nil {
			return 0, e
		}
		end := bytes.LastIndexByte(raw, ')')
		if end < 0 {
			return 0, ErrVisibility
		}
		parts := strings.Fields(string(raw[end+1:]))
		if len(parts) <= 19 {
			return 0, ErrVisibility
		}
		value, e := strconv.ParseUint(parts[19], 10, 64)
		if e != nil || value == 0 {
			return 0, ErrVisibility
		}
		return value, nil
	}
	ticks, err := start()
	if err != nil {
		return Process{}, err
	}
	exe, err := FileIdentity(filepath.Join(directory, "exe"))
	if err != nil {
		return Process{}, err
	}
	boot, err := read(filepath.Join(root, "sys/kernel/random/boot_id"), 128)
	if err != nil {
		return Process{}, err
	}
	status, err := read(filepath.Join(directory, "status"), 1024*1024)
	if err != nil {
		return Process{}, err
	}
	var uid uint64
	found := false
	for _, line := range strings.Split(string(status), "\n") {
		if strings.HasPrefix(line, "Uid:") {
			values := strings.Fields(line)
			if len(values) > 1 {
				uid, err = strconv.ParseUint(values[1], 10, 32)
				if err != nil {
					return Process{}, err
				}
				found = true
			}
			break
		}
	}
	if !found {
		return Process{}, ErrVisibility
	}
	command, err := read(filepath.Join(directory, "cmdline"), 4*1024*1024)
	if err != nil {
		return Process{}, err
	}
	args := []string{}
	for _, arg := range bytes.Split(command, []byte{0}) {
		if len(arg) > 0 {
			args = append(args, string(arg))
		}
	}
	env, err := read(filepath.Join(directory, "environ"), 4*1024*1024)
	if err != nil {
		return Process{}, err
	}
	after, err := start()
	if err != nil || after != ticks {
		return Process{}, ErrVisibility
	}
	return Process{pid, ticks, strings.ToLower(strings.TrimSpace(string(boot))), uint32(uid), exe, args, environment(env)}, nil
}
func socketInodes(root string, port int) (map[string]bool, error) {
	result := map[string]bool{}
	for _, name := range []string{"tcp", "tcp6"} {
		raw, err := read(filepath.Join(root, "net", name), 32*1024*1024)
		if err != nil {
			return nil, ErrVisibility
		}
		for _, line := range strings.Split(string(raw), "\n")[1:] {
			fields := strings.Fields(line)
			if len(fields) < 10 {
				continue
			}
			_, value, ok := strings.Cut(fields[1], ":")
			if !ok {
				continue
			}
			n, e := strconv.ParseUint(value, 16, 16)
			if e == nil && int(n) == port && fields[3] == "0A" {
				result[fields[9]] = true
			}
		}
	}
	return result, nil
}
func Listeners(_ context.Context, port int) (map[int]bool, error) {
	if port < 1 || port > 65535 {
		return nil, ErrVisibility
	}
	inodes, err := socketInodes("/proc", port)
	if err != nil {
		return nil, err
	}
	owners := map[int]bool{}
	if len(inodes) == 0 {
		return owners, nil
	}
	processes, err := os.ReadDir("/proc")
	if err != nil {
		return nil, ErrVisibility
	}
	remaining := map[string]bool{}
	for inode := range inodes {
		remaining[inode] = true
	}
	for _, process := range processes {
		pid, e := strconv.Atoi(process.Name())
		if e != nil {
			continue
		}
		fds, e := os.ReadDir(filepath.Join("/proc", process.Name(), "fd"))
		if e != nil {
			continue
		}
		for _, fd := range fds {
			target, e := os.Readlink(filepath.Join("/proc", process.Name(), "fd", fd.Name()))
			if e != nil {
				continue
			}
			if strings.HasPrefix(target, "socket:[") && strings.HasSuffix(target, "]") {
				inode := strings.TrimSuffix(strings.TrimPrefix(target, "socket:["), "]")
				if inodes[inode] {
					owners[pid] = true
					delete(remaining, inode)
				}
			}
		}
	}
	if len(remaining) > 0 {
		return nil, ErrVisibility
	}
	return owners, nil
}
func portStatus(_ context.Context, pid, port int) string {
	inodes, err := socketInodes("/proc", port)
	if err != nil {
		return "unknown"
	}
	if len(inodes) == 0 {
		return "unbound"
	}
	directory := filepath.Join("/proc", strconv.Itoa(pid), "fd")
	files, err := os.ReadDir(directory)
	if os.IsNotExist(err) {
		return "other"
	}
	if err != nil {
		return "unknown"
	}
	for _, file := range files {
		target, e := os.Readlink(filepath.Join(directory, file.Name()))
		if os.IsNotExist(e) {
			continue
		}
		if e != nil {
			return "unknown"
		}
		if strings.HasPrefix(target, "socket:[") && strings.HasSuffix(target, "]") {
			delete(inodes, strings.TrimSuffix(strings.TrimPrefix(target, "socket:["), "]"))
		}
	}
	if len(inodes) == 0 {
		return "owned"
	}
	return "other"
}
