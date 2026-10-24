package managed

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

func (c *Controller) prepareLog() (*os.File, error) {
	directory := filepath.Dir(c.Config.Log)
	if info, e := os.Lstat(directory); e == nil && !info.IsDir() {
		return nil, errors.New("symlink_rejected")
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(c.Config.Log, os.O_CREATE|os.O_WRONLY|os.O_APPEND|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0600)
	if err != nil {
		return nil, err
	}
	st, err := file.Stat()
	if err != nil || !st.Mode().IsRegular() {
		file.Close()
		return nil, errors.New("managed_log_unavailable")
	}
	if err = file.Chmod(0600); err != nil {
		file.Close()
		return nil, err
	}
	return file, nil
}
