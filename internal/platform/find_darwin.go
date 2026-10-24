//go:build darwin

package platform

import (
	"context"
	"strconv"
	"strings"
)

func ExecutableProcesses(ctx context.Context, path string) ([]Process, error) {
	target, err := FileIdentity(path)
	if err != nil {
		return nil, err
	}
	raw, _, err := command(ctx, "/bin/ps", "-axww", "-o", "pid=,comm=")
	if err != nil {
		return nil, err
	}
	rows := []Process{}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}
		if strings.TrimSpace(strings.TrimPrefix(line, parts[0])) != target.Path {
			continue
		}
		pid, err := strconv.Atoi(parts[0])
		if err != nil {
			return nil, ErrVisibility
		}
		p, err := Observe(ctx, pid)
		if err != nil {
			return nil, ErrVisibility
		}
		if p.Executable == target {
			rows = append(rows, p)
		}
	}
	return rows, nil
}
