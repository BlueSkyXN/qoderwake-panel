//go:build darwin

package managed

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type outputBuffer struct {
	bytes.Buffer
	max int
}

func (b *outputBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > b.max {
		return 0, errors.New("launchd_response_too_large")
	}
	return b.Buffer.Write(p)
}
func launchctl(ctx context.Context, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/launchctl", args...)
	out := &outputBuffer{max: 1024 * 1024}
	cmd.Stdout = out
	cmd.Stderr = out
	err := cmd.Run()
	return out.Bytes(), err
}
func digest(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }
func (c *Controller) label() string {
	return "com.qoderwake.panel." + c.Config.Name + "-" + digest([]byte(c.Config.Root + "\x00" + c.Config.Home))[:16]
}
func (c *Controller) plistPath() string {
	return filepath.Join(c.Config.Root, "process-state", c.Config.Name+".plist")
}
func domain() string { return "gui/" + strconv.Itoa(os.Geteuid()) }

var servicePID = regexp.MustCompile(`(?m)^\s*pid\s*=\s*([0-9]+)\s*$`)

func (c *Controller) service(ctx context.Context) (bool, int, error) {
	out, err := launchctl(ctx, "print", domain()+"/"+c.label())
	if err != nil {
		if bytes.Contains(out, []byte("Could not find service")) {
			return false, 0, nil
		}
		return false, 0, errors.New("managed_launchd_unavailable")
	}
	matches := servicePID.FindAllSubmatch(out, -1)
	if len(matches) == 0 {
		return true, 0, nil
	}
	if len(matches) != 1 {
		return false, 0, errors.New("managed_launchd_identity_unknown")
	}
	pid, e := strconv.Atoi(string(matches[0][1]))
	if e != nil || pid < 1 {
		return false, 0, errors.New("managed_launchd_identity_unknown")
	}
	return true, pid, nil
}
func (c *Controller) plist(environment map[string]string) []byte {
	var out bytes.Buffer
	out.WriteString(`<?xml version="1.0" encoding="UTF-8"?><!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd"><plist version="1.0"><dict>`)
	text := func(tag, value string) {
		out.WriteString("<" + tag + ">")
		xml.EscapeText(&out, []byte(value))
		out.WriteString("</" + tag + ">")
	}
	pair := func(key, value string) { text("key", key); text("string", value) }
	pair("Label", c.label())
	text("key", "ProgramArguments")
	out.WriteString("<array>")
	for _, value := range c.command() {
		text("string", value)
	}
	out.WriteString("</array>")
	pair("WorkingDirectory", c.Config.Root)
	pair("StandardOutPath", c.Config.Log)
	pair("StandardErrorPath", c.Config.Log)
	pair("ProcessType", "Background")
	text("key", "RunAtLoad")
	out.WriteString("<true/>")
	text("key", "KeepAlive")
	out.WriteString("<false/>")
	text("key", "EnvironmentVariables")
	out.WriteString("<dict>")
	values := map[string]string{}
	for _, entry := range c.childEnvironment(environment) {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			values[key] = value
		}
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		pair(key, values[key])
	}
	out.WriteString("</dict></dict></plist>")
	return out.Bytes()
}
func (c *Controller) verifySupervisor(value *Supervisor) error {
	if value == nil || value.Kind != "launchd" || value.Label != c.label() || value.Path != c.plistPath() {
		return errors.New("managed_launchd_identity_unknown")
	}
	file, err := os.OpenFile(value.Path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return errors.New("managed_launchd_configuration_changed")
	}
	defer file.Close()
	st, err := file.Stat()
	if err != nil || !st.Mode().IsRegular() || st.Size() > 1024*1024 {
		return errors.New("managed_launchd_configuration_changed")
	}
	raw, err := io.ReadAll(io.LimitReader(file, 1024*1024+1))
	if err != nil || len(raw) > 1024*1024 || digest(raw) != value.SHA256 {
		return errors.New("managed_launchd_configuration_changed")
	}
	return nil
}
func (c *Controller) bootout(ctx context.Context, value *Supervisor, pid int) error {
	if err := c.verifySupervisor(value); err != nil {
		return err
	}
	exists, current, err := c.service(ctx)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	if current != 0 && current != pid {
		return errors.New("managed_launchd_identity_unknown")
	}
	if _, err = launchctl(ctx, "bootout", domain()+"/"+c.label()); err != nil {
		return errors.New("managed_launchd_stop_failed")
	}
	// bootout 返回时服务可能仍在退出；等 label 卸载后才能用同一名称再次 bootstrap。
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		exists, current, err = c.service(ctx)
		if err != nil {
			return err
		}
		if !exists {
			return nil
		}
		if current != 0 && current != pid {
			return errors.New("managed_launchd_identity_unknown")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return errors.New("managed_launchd_stop_timeout")
		case <-ticker.C:
		}
	}
}
func (c *Controller) spawn(ctx context.Context, environment map[string]string) (int, *Supervisor, func() error, error) {
	exists, _, err := c.service(ctx)
	if err != nil {
		return 0, nil, nil, err
	}
	if exists {
		return 0, nil, nil, errors.New("unmanaged_launchd_service_exists")
	}
	file, err := c.prepareLog()
	if err != nil {
		return 0, nil, nil, err
	}
	file.Close()
	raw := c.plist(environment)
	if err = Atomic(c.plistPath(), raw); err != nil {
		return 0, nil, nil, err
	}
	supervisor := &Supervisor{Kind: "launchd", Label: c.label(), Path: c.plistPath(), SHA256: digest(raw)}
	if _, err = launchctl(ctx, "bootstrap", domain(), supervisor.Path); err != nil {
		return 0, nil, nil, errors.New("managed_launchd_start_failed")
	}
	pid := 0
	rollback := func() error {
		bounded, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return c.bootout(bounded, supervisor, pid)
	}
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		_, current, e := c.service(ctx)
		if e == nil && current > 0 {
			pid = current
			return pid, supervisor, rollback, nil
		}
		select {
		case <-ctx.Done():
			rollback()
			return 0, nil, nil, ctx.Err()
		case <-deadline.C:
			rollback()
			return 0, nil, nil, errors.New("managed_launchd_start_failed")
		case <-ticker.C:
		}
	}
}
func (c *Controller) terminate(ctx context.Context, state *State, check func() error) error {
	if err := c.verifySupervisor(state.Supervisor); err != nil {
		return err
	}
	if err := check(); err != nil {
		return err
	}
	return c.bootout(ctx, state.Supervisor, state.PID)
}
func (c *Controller) cleanupSupervisor(ctx context.Context, state *State) error {
	if state.Supervisor == nil {
		return errors.New("managed_launchd_identity_unknown")
	}
	return c.bootout(ctx, state.Supervisor, 0)
}
func (c *Controller) supervision(ctx context.Context, state *State) error {
	if err := c.verifySupervisor(state.Supervisor); err != nil {
		return err
	}
	exists, pid, err := c.service(ctx)
	if err != nil {
		return err
	}
	if !exists || pid != state.PID {
		return errors.New("managed_launchd_identity_unknown")
	}
	return nil
}
