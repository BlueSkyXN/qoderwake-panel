//go:build linux

package managed

import (
	"context"
	"errors"
	"os/exec"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

func (c *Controller) spawn(_ context.Context, environment map[string]string) (int, *Supervisor, func() error, error) {
	file, err := c.prepareLog()
	if err != nil {
		return 0, nil, nil, err
	}
	defer file.Close()
	cmd := exec.Command(c.Config.Launch, c.command()[1:]...)
	cmd.Env = c.childEnvironment(environment)
	cmd.Dir = c.Config.Root
	cmd.Stdout = file
	cmd.Stderr = file
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err = cmd.Start(); err != nil {
		return 0, nil, nil, err
	}
	done := make(chan struct{})
	go func() { cmd.Wait(); close(done) }()
	rollback := func() error {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		timer := time.NewTimer(10 * time.Second)
		defer timer.Stop()
		select {
		case <-done:
			return nil
		case <-timer.C:
			return errors.New("managed_process_stop_timeout")
		}
	}
	return cmd.Process.Pid, nil, rollback, nil
}
func (c *Controller) terminate(ctx context.Context, state *State, check func() error) error {
	fd, err := unix.PidfdOpen(state.PID, 0)
	if err != nil {
		return errors.New("managed_process_pidfd_required")
	}
	defer unix.Close(fd)
	if err = check(); err != nil {
		return err
	}
	if err = unix.PidfdSendSignal(fd, unix.SIGTERM, nil, 0); err != nil && !errors.Is(err, unix.ESRCH) {
		return errors.New("managed_process_signal_failed")
	}
	deadline := time.Now().Add(10 * time.Second)
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
	return errors.New("managed_process_stop_timeout")
}
func (c *Controller) cleanupSupervisor(context.Context, *State) error { return nil }
func (c *Controller) supervision(context.Context, *State) error       { return nil }
