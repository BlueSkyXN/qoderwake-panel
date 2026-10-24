package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"qoderwake-panel/internal/platform"
	"reflect"
	"runtime"
	"syscall"
	"time"
)

const recoveryTimeout = 60 * time.Second

func (m *Manager) discover(ctx context.Context, j *Journal) (*State, error) {
	if err := m.verifyGeneration(j.Candidate); err != nil {
		return nil, err
	}
	processes, err := platform.ExecutableProcesses(ctx, filepath.Join(j.Candidate.Path, "gateway"))
	if err != nil {
		return nil, err
	}
	var found *State
	for _, p := range processes {
		env := m.environment(j.Candidate, j.Nonce)
		matches := p.UID == m.UID && reflect.DeepEqual(p.Cmdline, m.command(j.Candidate))
		for key, value := range env {
			if p.Environment[key] != value {
				matches = false
			}
		}
		if !matches {
			continue
		}
		if found != nil {
			return nil, errors.New("gateway_recovery_identity_ambiguous")
		}
		var supervisor *Supervisor
		if runtime.GOOS == "darwin" {
			raw, err := regular(m.plistPath(), 1024*1024)
			if err != nil {
				return nil, err
			}
			sum := sha256.Sum256(raw)
			supervisor = &Supervisor{m.plistPath(), m.label(), hex.EncodeToString(sum[:])}
		}
		var previous *Generation
		if j.Previous != nil {
			previous = &j.Previous.Generation
		}
		found, err = m.capture(ctx, p.PID, j.Candidate, j.Nonce, supervisor, previous)
		if err != nil {
			return nil, err
		}
	}
	return found, nil
}
func (m *Manager) recover(ctx context.Context) error {
	j, err := m.readJournal()
	if err != nil {
		return err
	}
	if j == nil {
		return nil
	}
	originalCandidate := j.Candidate
	if j.Phase == "committing" && j.Started != nil && m.healthy(ctx, j.Started) {
		if err = m.publish(ctx, j.Started); err != nil {
			return err
		}
		return m.clearJournal()
	}
	candidate := j.Started
	if candidate == nil {
		candidate, err = m.discover(ctx, j)
		if err != nil {
			return err
		}
	}
	if candidate != nil {
		if err = m.stop(ctx, candidate); err != nil {
			return err
		}
	}
	if j.Previous != nil {
		if m.healthy(ctx, j.Previous) {
			if err = m.publish(ctx, j.Previous); err != nil {
				return err
			}
		} else {
			identity := m.identity(ctx, j.Previous)
			if identity != "exited" {
				return errors.New("gateway_recovery_identity_unknown")
			}
			if !m.portFree(ctx) {
				return errors.New("gateway_listener_identity_unknown")
			}
			if err = m.cleanupSupervisor(ctx, j.Previous); err != nil {
				return err
			}
			if err = m.preflight(ctx, j.Previous.Generation); err != nil {
				return err
			}
			restored, e := m.start(ctx, j.Previous.Generation, token(16), j.Previous.Previous)
			if e != nil {
				return errors.New("gateway_recovery_failed")
			}
			// 恢复进程也写入日志后再发布，下一次恢复以其真实身份为准。
			j.Candidate = restored.Generation
			j.Started = restored
			j.Nonce = restored.Nonce
			j.Phase = "committing"
			if err = m.writeJSON("operation.json", j); err != nil {
				return err
			}
			if err = m.publish(ctx, restored); err != nil {
				return err
			}
		}
	} else if !m.portFree(ctx) {
		return errors.New("gateway_listener_identity_unknown")
	}
	if err = m.clearJournal(); err != nil {
		return err
	}
	_ = m.removeGeneration(originalCandidate)
	return nil
}
func (m *Manager) Status(ctx context.Context) map[string]any {
	state, err := m.readState()
	if err != nil {
		return map[string]any{"managed": true, "healthy": false, "error": err.Error(), "legacy": err.Error() == "legacy_gateway_migration_required"}
	}
	if state == nil {
		free := m.portFree(ctx)
		return map[string]any{"managed": false, "healthy": false, "rollbackAvailable": false, "canStart": free, "listening": !free}
	}
	healthy := m.healthy(ctx, state)
	result := map[string]any{"managed": true, "healthy": healthy, "rollbackAvailable": state.Previous != nil, "mode": nil, "configHash": nil, "recoveryRequired": false}
	if _, err := os.Lstat(filepath.Join(m.store(), "operation.json")); !os.IsNotExist(err) {
		result["recoveryRequired"] = true
	}
	if healthy {
		result["mode"] = state.Generation.Mode
		result["configHash"] = state.Generation.ConfigHash
	}
	return result
}
func (m *Manager) Preflight(ctx context.Context, p Policy) (map[string]any, error) {
	release, err := m.locked()
	if err != nil {
		return nil, err
	}
	defer release()
	g, err := m.snapshot(p)
	if g.Path != "" {
		defer m.removeGeneration(g)
	}
	if err != nil {
		return nil, err
	}
	if err = m.preflight(ctx, g); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "mode": p.Mode, "configHash": p.Hash(), "activeUnchanged": true}, nil
}
func (m *Manager) Apply(ctx context.Context, p Policy) (map[string]any, error) {
	release, err := m.locked()
	if err != nil {
		return nil, err
	}
	defer release()
	if err = m.recover(ctx); err != nil {
		return nil, err
	}
	previous, err := m.readState()
	if err != nil {
		return nil, err
	}
	g, err := m.snapshot(p)
	if g.Path != "" {
		defer m.removeGeneration(g)
	}
	if err != nil {
		return nil, err
	}
	return m.transition(ctx, "apply", g, previous)
}
func (m *Manager) Rollback(ctx context.Context) (map[string]any, error) {
	release, err := m.locked()
	if err != nil {
		return nil, err
	}
	defer release()
	if err = m.recover(ctx); err != nil {
		return nil, err
	}
	current, err := m.readState()
	if err != nil {
		return nil, err
	}
	if current == nil || current.Previous == nil {
		return nil, errors.New("gateway_rollback_unavailable")
	}
	if !m.healthy(ctx, current) {
		return nil, errors.New("gateway_process_identity_unknown")
	}
	return m.transition(ctx, "rollback", *current.Previous, current)
}
func (m *Manager) transition(ctx context.Context, action string, g Generation, previous *State) (map[string]any, error) {
	if err := m.preflight(ctx, g); err != nil {
		return nil, err
	}
	if !m.portFree(ctx) && (previous == nil || !m.healthy(ctx, previous)) {
		return nil, errors.New("unmanaged_gateway_running_migration_required")
	}
	if previous != nil {
		if err := m.preflight(ctx, previous.Generation); err != nil {
			return nil, err
		}
	}
	j := &Journal{SchemaVersion: ProtocolVersion, OperationID: token(16), Action: action, Phase: "prepared", Candidate: g, Previous: previous, Nonce: token(16)}
	if err := m.writeJSON("operation.json", j); err != nil {
		return nil, err
	}
	if previous != nil {
		if err := m.stop(ctx, previous); err != nil {
			return nil, err
		}
	}
	j.Phase = "starting"
	if err := m.writeJSON("operation.json", j); err != nil {
		return nil, err
	}
	var previousGeneration *Generation
	if previous != nil {
		previousGeneration = &previous.Generation
	}
	current, err := m.start(ctx, g, j.Nonce, previousGeneration)
	if err != nil {
		// 保存事务后才进行任何恢复；不能以调用失败推断新进程已经退出。
		bounded, cancel := context.WithTimeout(context.WithoutCancel(ctx), recoveryTimeout)
		defer cancel()
		if e := m.recover(bounded); e != nil {
			return nil, errors.New("gateway_apply_failed_rollback_failed")
		}
		if previous != nil {
			return nil, errors.New("gateway_apply_failed_previous_restored")
		}
		return nil, err
	}
	j.Phase = "committing"
	j.Started = current
	if err = m.writeJSON("operation.json", j); err != nil {
		return nil, errors.New("gateway_publish_commit_unknown")
	}
	if err = m.publish(ctx, current); err != nil {
		return nil, err
	}
	if err = m.clearJournal(); err != nil {
		return nil, errors.New("gateway_commit_cleanup_failed")
	}
	_, _ = m.gc(ctx)
	return map[string]any{"ok": true, "mode": g.Mode, "configHash": g.ConfigHash, "pid": current.Process.PID}, nil
}
func (m *Manager) Recover(ctx context.Context) error {
	release, err := m.locked()
	if err != nil {
		return err
	}
	defer release()
	return m.recover(ctx)
}
func (m *Manager) Stop(ctx context.Context) error {
	release, err := m.locked()
	if err != nil {
		return err
	}
	defer release()
	if err = m.recover(ctx); err != nil {
		return err
	}
	state, err := m.readState()
	if err != nil {
		return err
	}
	if state == nil {
		if !m.portFree(ctx) {
			return errors.New("unmanaged_gateway_running_migration_required")
		}
		return nil
	}
	if err = m.stop(ctx, state); err != nil {
		return err
	}
	if err = os.Remove(filepath.Join(m.store(), "current.json")); err != nil && !errors.Is(err, syscall.ENOENT) {
		return err
	}
	return nil
}
