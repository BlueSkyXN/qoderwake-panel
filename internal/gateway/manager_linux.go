//go:build linux

package gateway

import (
	"context"
	"errors"
	"golang.org/x/sys/unix"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

func (m *Manager) commandCredentials(cmd *exec.Cmd) error {
	if os.Geteuid() != 0 && uint32(os.Geteuid()) != m.UID {
		return errors.New("gateway_user_unavailable")
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	if os.Geteuid() == 0 {
		cmd.SysProcAttr.Credential = &syscall.Credential{Uid: m.UID, Gid: m.GID, Groups: []uint32{}}
	}
	return nil
}
func (m *Manager) spawn(ctx context.Context, g Generation, nonce string) (int, *Supervisor, func() error, error) {
	file, err := os.OpenFile(filepath.Join(m.Root, "logs", "gateway.stdout.log"), os.O_WRONLY|os.O_APPEND|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return 0, nil, nil, err
	}
	defer file.Close()
	cmd := exec.Command(filepath.Join(g.Path, "gateway"), m.command(g)[1:]...)
	cmd.Env = environmentList(m.environment(g, nonce))
	cmd.Dir = g.Path
	cmd.Stdout = file
	cmd.Stderr = file
	if err = m.commandCredentials(cmd); err != nil {
		return 0, nil, nil, err
	}
	cmd.SysProcAttr.Setsid = true
	if err = cmd.Start(); err != nil {
		return 0, nil, nil, err
	}
	done := make(chan struct{})
	go func() { cmd.Wait(); close(done) }()
	rollback := func() error {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-done:
			return nil
		case <-time.After(15 * time.Second):
			return errors.New("gateway_stop_timeout")
		}
	}
	return cmd.Process.Pid, nil, rollback, nil
}
func (m *Manager) terminate(ctx context.Context, s *State, check func() error) error {
	fd, err := unix.PidfdOpen(s.Process.PID, 0)
	if err != nil {
		return errors.New("gateway_pidfd_required")
	}
	defer unix.Close(fd)
	if err = check(); err != nil {
		return err
	}
	if err = unix.PidfdSendSignal(fd, unix.SIGTERM, nil, 0); err != nil && !errors.Is(err, unix.ESRCH) {
		return errors.New("gateway_signal_failed")
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
		n, e := unix.Poll(fds, 100)
		if errors.Is(e, unix.EINTR) {
			continue
		}
		if e != nil {
			return e
		}
		if n > 0 {
			return nil
		}
	}
	return errors.New("gateway_stop_timeout")
}
func (m *Manager) cleanupSupervisor(context.Context, *State) error { return nil }
func (m *Manager) supervision(context.Context, *State) error       { return nil }
