//go:build darwin

package gateway

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"qoderwake-panel/internal/service"
	"regexp"
	"strconv"
	"time"
)

func (m *Manager) commandCredentials(cmd *exec.Cmd) error {
	if m.UID != uint32(os.Geteuid()) {
		return errors.New("gateway_user_unavailable")
	}
	return nil
}
func launchctl(ctx context.Context, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/launchctl", args...)
	out := &limitedBuffer{max: 1024 * 1024}
	cmd.Stdout = out
	cmd.Stderr = out
	err := cmd.Run()
	return out.Bytes(), err
}
func (m *Manager) domain() string { return "gui/" + strconv.Itoa(int(m.UID)) }

var servicePID = regexp.MustCompile(`(?m)^\s*pid\s*=\s*([0-9]+)\s*$`)

func (m *Manager) service(ctx context.Context) (bool, int, error) {
	raw, err := launchctl(ctx, "print", m.domain()+"/"+m.label())
	if err != nil {
		if bytes.Contains(raw, []byte("Could not find service")) {
			return false, 0, nil
		}
		return false, 0, errors.New("gateway_launchd_unavailable")
	}
	matches := servicePID.FindAllSubmatch(raw, -1)
	if len(matches) == 0 {
		return true, 0, nil
	}
	if len(matches) != 1 {
		return false, 0, errors.New("gateway_launchd_identity_unknown")
	}
	pid, err := strconv.Atoi(string(matches[0][1]))
	if err != nil || pid < 1 {
		return false, 0, errors.New("gateway_launchd_identity_unknown")
	}
	return true, pid, nil
}
func (m *Manager) verifySupervisor(s *Supervisor) error {
	if s == nil || s.Label != m.label() || s.Path != m.plistPath() {
		return errors.New("gateway_launchd_identity_unknown")
	}
	raw, err := regular(s.Path, 1024*1024)
	if err != nil {
		return errors.New("gateway_launchd_configuration_changed")
	}
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != s.Hash {
		return errors.New("gateway_launchd_configuration_changed")
	}
	return nil
}
func (m *Manager) bootout(ctx context.Context, s *Supervisor, pid int) error {
	if err := m.verifySupervisor(s); err != nil {
		return err
	}
	exists, current, err := m.service(ctx)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	if current != 0 && current != pid {
		return errors.New("gateway_launchd_identity_unknown")
	}
	if _, err = launchctl(ctx, "bootout", m.domain()+"/"+m.label()); err != nil {
		return errors.New("gateway_launchd_stop_failed")
	}
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		exists, current, err = m.service(ctx)
		if err != nil {
			return err
		}
		if !exists {
			return nil
		}
		if current != 0 && current != pid {
			return errors.New("gateway_launchd_identity_unknown")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return errors.New("gateway_stop_timeout")
		case <-ticker.C:
		}
	}
}
func (m *Manager) spawn(ctx context.Context, g Generation, nonce string) (int, *Supervisor, func() error, error) {
	exists, _, err := m.service(ctx)
	if err != nil {
		return 0, nil, nil, err
	}
	if exists {
		return 0, nil, nil, errors.New("unmanaged_gateway_launchd_service_exists")
	}
	cfg := service.Config{Platform: "darwin", Executable: filepath.Join(g.Path, "gateway"), Root: m.Root, Home: m.Root, Bind: "127.0.0.1", Port: m.Port, Label: m.label(), Environment: m.environment(g, nonce), Arguments: m.command(g), LogPrefix: "gateway", DisableKeepAlive: true}
	raw, err := cfg.Render()
	if err != nil {
		return 0, nil, nil, err
	}
	if err = writeAtomic(m.plistPath(), raw, 0600, m.GID); err != nil {
		return 0, nil, nil, err
	}
	sum := sha256.Sum256(raw)
	supervisor := &Supervisor{m.plistPath(), m.label(), hex.EncodeToString(sum[:])}
	rollback := func() error {
		bounded, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		exists, current, err := m.service(bounded)
		if err != nil {
			return err
		}
		if !exists {
			return nil
		}
		if current > 0 {
			state, err := m.capture(bounded, current, g, nonce, supervisor, nil)
			if err != nil {
				return err
			}
			return m.stop(bounded, state)
		}
		return m.bootout(bounded, supervisor, 0)
	}
	if _, err = launchctl(ctx, "bootstrap", m.domain(), supervisor.Path); err != nil {
		_ = rollback()
		return 0, nil, nil, errors.New("gateway_launchd_start_failed")
	}
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		_, current, err := m.service(ctx)
		if err == nil && current > 0 {
			return current, supervisor, rollback, nil
		}
		select {
		case <-ctx.Done():
			_ = rollback()
			return 0, nil, nil, ctx.Err()
		case <-deadline.C:
			_ = rollback()
			return 0, nil, nil, errors.New("gateway_launchd_start_failed")
		case <-ticker.C:
		}
	}
}
func (m *Manager) terminate(ctx context.Context, s *State, check func() error) error {
	if err := check(); err != nil {
		return err
	}
	return m.bootout(ctx, s.Supervisor, s.Process.PID)
}
func (m *Manager) cleanupSupervisor(ctx context.Context, s *State) error {
	exists, _, err := m.service(ctx)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	return m.bootout(ctx, s.Supervisor, 0)
}
func (m *Manager) supervision(ctx context.Context, s *State) error {
	if err := m.verifySupervisor(s.Supervisor); err != nil {
		return err
	}
	exists, pid, err := m.service(ctx)
	if err != nil || !exists || pid != s.Process.PID {
		return errors.New("gateway_launchd_identity_unknown")
	}
	return nil
}
