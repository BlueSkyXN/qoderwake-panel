package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

type Executor func(context.Context, string, ...string) ([]byte, error)
type Firewall struct {
	Root           string
	UID            uint32
	RequiredDirect []string
	Path           string
	Platform       string
	Execute        Executor
}

func NewFirewall(root string, uid uint32) *Firewall {
	required := os.Getenv("QW_REQUIRED_DIRECT")
	if required == "" {
		required = "47.86.184.84 47.86.185.172"
	}
	path, _ := exec.LookPath("iptables")
	return &Firewall{Root: root, UID: uid, RequiredDirect: strings.Fields(required), Path: path, Platform: runtime.GOOS, Execute: func(ctx context.Context, name string, args ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, name, args...)
		out := &limitedBuffer{max: 65536}
		cmd.Stdout = out
		cmd.Stderr = out
		err := cmd.Run()
		return out.Bytes(), err
	}}
}
func (f *Firewall) targets() ([]string, string, error) {
	raw, err := regular(filepath.Join(f.Root, "config", "cp-ips.txt"), 65536)
	if os.IsNotExist(err) {
		raw = nil
	} else if err != nil {
		return nil, "", err
	}
	ips := []string{}
	seen := map[string]bool{}
	for _, line := range strings.Split(string(raw), "\n") {
		line, _, _ = strings.Cut(line, "#")
		for _, word := range strings.Fields(line) {
			ip, err := netip.ParseAddr(word)
			if err != nil || !ip.Is4() || ip.IsUnspecified() || ip.IsLoopback() {
				return nil, "", errors.New("invalid_control_plane_ips")
			}
			value := ip.String()
			if !seen[value] {
				ips = append(ips, value)
				seen[value] = true
			}
		}
	}
	if len(ips) > 200 {
		return nil, "", errors.New("invalid_control_plane_ips")
	}
	sum := sha256.Sum256(append(append(raw, 0), []byte(strings.Join(f.RequiredDirect, " ")+":"+strconv.Itoa(int(f.UID)))...))
	return ips, hex.EncodeToString(sum[:]), nil
}
func (f *Firewall) run(ctx context.Context, verb string, rule []string) (bool, error) {
	args := append([]string{"-w", "5", verb, "OUTPUT"}, rule...)
	_, err := f.Execute(ctx, f.Path, args...)
	if err == nil {
		return true, nil
	}
	var exit *exec.ExitError
	if verb == "-C" && errors.As(err, &exit) && exit.ExitCode() == 1 {
		return false, nil
	}
	// 合成执行器可用此错误表示规则不存在。
	if verb == "-C" && errors.Is(err, ErrRuleMissing) {
		return false, nil
	}
	return false, errors.New("firewall_command_failed")
}

var ErrRuleMissing = errors.New("firewall_rule_missing")

func ownerRule(ip string, uid uint32) []string {
	return []string{"-p", "tcp", "-d", ip, "--dport", "443", "-m", "owner", "--uid-owner", strconv.Itoa(int(uid)), "-j", "ACCEPT"}
}
func denyRule(ip string) []string {
	return []string{"-p", "tcp", "-d", ip, "--dport", "443", "-j", "REJECT", "--reject-with", "tcp-reset"}
}
func dnsRule(protocol string) []string {
	return []string{"-d", "203.107.1.1", "-p", protocol, "-j", "DROP"}
}
func (f *Firewall) Status(ctx context.Context) map[string]any {
	result := map[string]any{"supported": f.Platform == "linux", "available": false, "active_rules": 0, "targets": []string{}, "requiredDirect": f.RequiredDirect}
	if f.Platform != "linux" {
		return result
	}
	if f.Path == "" {
		result["error"] = "iptables_unavailable"
		return result
	}
	result["available"] = true
	ips, revision, err := f.targets()
	if err != nil {
		result["error"] = "invalid_control_plane_ips"
		result["active_rules"] = -1
		return result
	}
	result["targets"] = ips
	result["revision"] = revision
	count := 0
	for _, ip := range ips {
		exists, e := f.run(ctx, "-C", denyRule(ip))
		if e != nil {
			result["error"] = e.Error()
			result["active_rules"] = -1
			return result
		}
		if exists {
			count++
		}
	}
	exists, err := f.run(ctx, "-C", dnsRule("udp"))
	if err != nil {
		result["error"] = err.Error()
		result["active_rules"] = -1
		return result
	}
	if exists {
		count++
	}
	result["active_rules"] = count
	return result
}
func (f *Firewall) Apply(ctx context.Context, action, revision string) (map[string]any, error) {
	if f.Platform != "linux" {
		return nil, errors.New("firewall_platform_unsupported")
	}
	if f.Path == "" {
		return nil, errors.New("iptables_unavailable")
	}
	if action != "add" && action != "remove" {
		return nil, errors.New("invalid_firewall_action")
	}
	ips, current, err := f.targets()
	if err != nil {
		return nil, err
	}
	if len(ips) == 0 {
		return nil, errors.New("control_plane_ips_required")
	}
	if revision != "" && revision != current {
		return nil, errors.New("version_conflict")
	}
	required := map[string]bool{}
	for _, ip := range f.RequiredDirect {
		required[ip] = true
	}
	rules := [][]string{}
	for _, ip := range ips {
		if action == "add" && required[ip] {
			continue
		}
		rules = append(rules, ownerRule(ip, f.UID), denyRule(ip))
	}
	rules = append(rules, dnsRule("udp"), dnsRule("tcp"))
	changed := 0
	failure := func(err error) (map[string]any, error) {
		if changed > 0 {
			return nil, errors.New("firewall_partial_failure")
		}
		return nil, err
	}
	for _, rule := range rules {
		if ctx.Err() != nil {
			return failure(ctx.Err())
		}
		exists, err := f.run(ctx, "-C", rule)
		if err != nil {
			return failure(err)
		}
		if action == "add" {
			if !exists {
				if _, err = f.run(ctx, "-A", rule); err != nil {
					return failure(err)
				}
				changed++
			}
			continue
		}
		for attempts := 0; exists; attempts++ {
			if attempts >= 200 {
				return failure(errors.New("firewall_duplicate_rule_limit"))
			}
			if _, err = f.run(ctx, "-D", rule); err != nil {
				return failure(err)
			}
			changed++
			exists, err = f.run(ctx, "-C", rule)
			if err != nil {
				return failure(err)
			}
		}
	}
	return map[string]any{"ok": true, "changed": changed}, nil
}
