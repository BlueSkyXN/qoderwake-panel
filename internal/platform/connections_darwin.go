//go:build darwin

package platform

import (
	"context"
	"strconv"
	"strings"
)

func Connections(ctx context.Context, pid int) ([]Connection, error) {
	raw, _, err := command(ctx, "/usr/sbin/lsof", "-nP", "-a", "-p", strconv.Itoa(pid), "-iTCP", "-FfnT")
	if err != nil {
		return nil, err
	}
	return parseConnections(string(raw)), nil
}
func parseConnections(raw string) []Connection {
	result := []Connection{}
	destination, state := "", ""
	flush := func() {
		if destination != "" {
			if row, ok := remoteConnection(destination, state); ok {
				result = append(result, row)
			}
		}
		destination, state = "", ""
	}
	for _, line := range strings.Split(raw, "\n") {
		if strings.HasPrefix(line, "f") {
			flush()
		}
		if strings.HasPrefix(line, "n") {
			_, destination, _ = strings.Cut(line[1:], "->")
		}
		if strings.HasPrefix(line, "TST=") {
			state = strings.TrimPrefix(line, "TST=")
		}
	}
	flush()
	return result
}
