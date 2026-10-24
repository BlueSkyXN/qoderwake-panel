//go:build darwin

package platform

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"os"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

func processArgs(raw []byte) (string, []string, map[string]string, error) {
	if len(raw) < 5 || len(raw) > 4*1024*1024 {
		return "", nil, nil, ErrVisibility
	}
	argc := int(binary.LittleEndian.Uint32(raw[:4]))
	if argc < 1 || argc > 4096 {
		return "", nil, nil, ErrVisibility
	}
	raw = raw[4:]
	end := bytes.IndexByte(raw, 0)
	if end < 1 {
		return "", nil, nil, ErrVisibility
	}
	path := string(raw[:end])
	raw = raw[end+1:]
	for len(raw) > 0 && raw[0] == 0 {
		raw = raw[1:]
	}
	args := make([]string, 0, argc)
	for range argc {
		end = bytes.IndexByte(raw, 0)
		if end < 0 {
			return "", nil, nil, ErrVisibility
		}
		args = append(args, string(raw[:end]))
		raw = raw[end+1:]
	}
	return path, args, environment(raw), nil
}
func birth(pid int) (Process, error) {
	info, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		if errors.Is(unix.Kill(pid, 0), unix.ESRCH) {
			return Process{}, os.ErrNotExist
		}
		return Process{}, ErrVisibility
	}
	boot, err := unix.Sysctl("kern.bootsessionuuid")
	if err != nil || boot == "" {
		return Process{}, ErrVisibility
	}
	if int(info.Proc.P_pid) != pid || info.Proc.P_starttime.Sec <= 0 {
		return Process{}, ErrVisibility
	}
	return Process{PID: pid, UID: info.Eproc.Ucred.Uid, StartTicks: uint64(info.Proc.P_starttime.Sec)*1000000 + uint64(info.Proc.P_starttime.Usec), BootID: strings.ToLower(strings.TrimSpace(boot))}, nil
}
func processExecutable(ctx context.Context, pid int, path string) (File, error) {
	raw, status, err := command(ctx, "/usr/sbin/lsof", "-nP", "-a", "-p", strconv.Itoa(pid), "-d", "txt", "-F0pDinf")
	if err != nil || status != 0 {
		return File{}, ErrVisibility
	}
	wanted := normalizedPath(path)
	current := File{}
	matchedPID := false
	for _, rawField := range bytes.Split(raw, []byte{0}) {
		field := strings.TrimLeft(string(rawField), "\n")
		if field == "" {
			continue
		}
		switch field[0] {
		case 'p':
			id, e := strconv.Atoi(field[1:])
			matchedPID = e == nil && id == pid
		case 'f':
			current = File{}
		case 'D':
			current.Device, _ = strconv.ParseUint(field[1:], 0, 64)
		case 'i':
			current.Inode, _ = strconv.ParseUint(field[1:], 10, 64)
		case 'n':
			current.Path = normalizedPath(field[1:])
			if matchedPID && current.Path == wanted && current.Inode > 0 && current.Device > 0 {
				return current, nil
			}
		}
	}
	return File{}, ErrVisibility
}
func Observe(ctx context.Context, pid int) (Process, error) {
	if pid < 1 {
		return Process{}, ErrVisibility
	}
	observed, err := birth(pid)
	if err != nil {
		return Process{}, err
	}
	raw, err := unix.SysctlRaw("kern.procargs2", pid)
	if err != nil {
		return Process{}, ErrVisibility
	}
	path, args, env, err := processArgs(raw)
	if err != nil {
		return Process{}, err
	}
	executable, err := processExecutable(ctx, pid, path)
	if err != nil {
		return Process{}, err
	}
	after, err := birth(pid)
	if err != nil || !SameBirth(observed, after) {
		return Process{}, ErrVisibility
	}
	observed.Executable = executable
	observed.Cmdline = args
	observed.Environment = env
	return observed, nil
}
func Listeners(ctx context.Context, port int) (map[int]bool, error) {
	if port < 1 || port > 65535 {
		return nil, ErrVisibility
	}
	raw, status, err := command(ctx, "/usr/sbin/lsof", "-nP", "-a", "-iTCP:"+strconv.Itoa(port), "-sTCP:LISTEN", "-F0p")
	if err != nil {
		return nil, err
	}
	owners := map[int]bool{}
	for _, item := range bytes.Split(raw, []byte{0}) {
		field := strings.TrimSpace(string(item))
		if strings.HasPrefix(field, "p") {
			pid, e := strconv.Atoi(field[1:])
			if e != nil || pid < 1 {
				return nil, ErrVisibility
			}
			owners[pid] = true
		}
	}
	if len(owners) == 0 && (status == 0 || portOpen(port)) {
		return nil, ErrVisibility
	}
	return owners, nil
}
func portStatus(ctx context.Context, pid, port int) string { return listenerStatus(ctx, pid, port) }
