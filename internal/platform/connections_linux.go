//go:build linux

package platform

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func Connections(ctx context.Context, pid int) ([]Connection, error) {
	root := filepath.Join("/proc", strconv.Itoa(pid))
	fds, err := os.ReadDir(filepath.Join(root, "fd"))
	if err != nil {
		return nil, ErrVisibility
	}
	owned := map[string]bool{}
	for _, fd := range fds {
		target, err := os.Readlink(filepath.Join(root, "fd", fd.Name()))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, ErrVisibility
		}
		if strings.HasPrefix(target, "socket:[") && strings.HasSuffix(target, "]") {
			owned[strings.TrimSuffix(strings.TrimPrefix(target, "socket:["), "]")] = true
		}
	}
	result := []Connection{}
	for _, name := range []string{"tcp", "tcp6"} {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		raw, err := read(filepath.Join(root, "net", name), 32*1024*1024)
		if os.IsNotExist(err) && name == "tcp6" {
			continue
		}
		if err != nil {
			return nil, ErrVisibility
		}
		for _, line := range strings.Split(string(raw), "\n") {
			fields := strings.Fields(line)
			if len(fields) < 10 || !owned[fields[9]] || fields[3] == "0A" {
				continue
			}
			destination, ok := procEndpoint(fields[2])
			if !ok {
				return nil, ErrVisibility
			}
			if row, ok := remoteConnection(destination, fields[3]); ok {
				result = append(result, row)
			}
		}
	}
	return result, nil
}
func procEndpoint(value string) (string, bool) {
	address, port, ok := strings.Cut(value, ":")
	if !ok {
		return "", false
	}
	raw, err := hex.DecodeString(address)
	if err != nil || (len(raw) != 4 && len(raw) != 16) {
		return "", false
	}
	for i := 0; i < len(raw); i += 4 {
		word := binary.BigEndian.Uint32(raw[i : i+4])
		binary.NativeEndian.PutUint32(raw[i:i+4], word)
	}
	ip, ok := netip.AddrFromSlice(raw)
	if !ok {
		return "", false
	}
	n, err := strconv.ParseUint(port, 16, 16)
	if err != nil {
		return "", false
	}
	return net.JoinHostPort(ip.String(), strconv.Itoa(int(n))), true
}
