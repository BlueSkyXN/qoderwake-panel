//go:build linux

package patches

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
)

func requireStopped(ctx context.Context, binary string) error {
	target, err := os.Stat(binary)
	if err != nil {
		return errors.New("patch_binary_unavailable")
	}
	processes, err := os.ReadDir("/proc")
	if err != nil {
		return errors.New("patch_process_visibility_required")
	}
	for _, process := range processes {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if _, err := strconv.Atoi(process.Name()); err != nil {
			continue
		}
		info, err := os.Stat(filepath.Join("/proc", process.Name(), "exe"))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return errors.New("patch_process_visibility_required")
		}
		if os.SameFile(target, info) {
			return errors.New("stop_daemon_before_patch")
		}
	}
	return nil
}
