//go:build darwin

package cli

import (
	"golang.org/x/sys/unix"
	"os"
)

func disableEcho(file *os.File) error {
	attrs, err := unix.IoctlGetTermios(int(file.Fd()), unix.TIOCGETA)
	if err != nil {
		return err
	}
	attrs.Lflag &^= unix.ECHO | unix.ECHONL
	return unix.IoctlSetTermios(int(file.Fd()), unix.TIOCSETA, attrs)
}
