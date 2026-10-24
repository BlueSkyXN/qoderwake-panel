//go:build darwin

package platform

import (
	"encoding/binary"
	"reflect"
	"testing"
)

func TestDarwinArgsPreserveSpacesAndEmptyArguments(t *testing.T) {
	raw := make([]byte, 4)
	binary.LittleEndian.PutUint32(raw, 4)
	raw = append(raw, []byte("/synthetic/Daemon\x00\x00\x00/synthetic/Daemon\x00start\x00\x00space value\x00QODERWAKE_HOME=/synthetic/home\x00VALUE=a=b c\x00\x00")...)
	path, args, env, err := processArgs(raw)
	if err != nil || path != "/synthetic/Daemon" || !reflect.DeepEqual(args, []string{"/synthetic/Daemon", "start", "", "space value"}) || env["VALUE"] != "a=b c" {
		t.Fatal("Darwin process arguments not preserved", err)
	}
	binary.LittleEndian.PutUint32(raw, 5000)
	if _, _, _, err = processArgs(raw); err == nil {
		t.Fatal("invalid argc accepted")
	}
}
