//go:build linux

package platform

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
)

func ExecutableProcesses(ctx context.Context, path string) ([]Process, error) {
	target, err := FileIdentity(path)
	if err != nil {
		return nil, err
	}
	rows := []Process{}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		file, err := FileIdentity(filepath.Join("/proc", entry.Name(), "exe"))
		if err != nil || file != target {
			continue
		}
		process, err := Observe(ctx, pid)
		if err != nil {
			return nil, ErrVisibility
		}
		rows = append(rows, process)
	}
	return rows, nil
}
