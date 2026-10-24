package gateway

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFirewallLegacyLiteRulesAndRemoval(t *testing.T) {
	root := t.TempDir()
	root, _ = filepath.EvalSymlinks(root)
	os.Mkdir(filepath.Join(root, "config"), 0700)
	os.WriteFile(filepath.Join(root, "config", "cp-ips.txt"), []byte("47.86.184.84\n203.0.113.10 # edge\n"), 0600)
	rules := map[string]int{}
	calls := []string{}
	f := NewFirewall(root, 701)
	f.Platform = "linux"
	f.Path = "synthetic-iptables"
	f.Execute = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name != "synthetic-iptables" {
			t.Fatal("unexpected executable")
		}
		if args[0] != "-w" || args[1] != "5" || args[3] != "OUTPUT" {
			t.Fatal("invalid command")
		}
		verb := args[2]
		key := strings.Join(args[4:], " ")
		calls = append(calls, verb+" "+key)
		switch verb {
		case "-C":
			if rules[key] == 0 {
				return nil, ErrRuleMissing
			}
		case "-A":
			rules[key]++
		case "-D":
			rules[key]--
		default:
			t.Fatal("unexpected verb", verb)
		}
		return nil, nil
	}
	state := f.Status(context.Background())
	revision := state["revision"].(string)
	result, err := f.Apply(context.Background(), "add", revision)
	if err != nil || result["changed"] != 4 {
		t.Fatal(result, err)
	}
	for key := range rules {
		if strings.Contains(key, "47.86.184.84") {
			t.Fatal("required direct endpoint blocked")
		}
	}
	if state = f.Status(context.Background()); state["active_rules"] != 2 {
		t.Fatal("legacy active count drift", state)
	}
	result, err = f.Apply(context.Background(), "add", revision)
	if err != nil || result["changed"] != 0 {
		t.Fatal("duplicate added", err)
	}
	rules[strings.Join(denyRule("203.0.113.10"), " ")] = 2
	if _, err = f.Apply(context.Background(), "remove", revision); err != nil {
		t.Fatal(err)
	}
	for key, count := range rules {
		if count != 0 {
			t.Fatal("rule left", key, count)
		}
	}
	before := len(calls)
	if _, err = f.Apply(context.Background(), "add", "stale"); err == nil || err.Error() != "version_conflict" || len(calls) != before {
		t.Fatal("stale targets applied")
	}
}
func TestFirewallFailsWithoutClaimingAllRulesApplied(t *testing.T) {
	root := t.TempDir()
	root, _ = filepath.EvalSymlinks(root)
	os.Mkdir(filepath.Join(root, "config"), 0700)
	os.WriteFile(filepath.Join(root, "config", "cp-ips.txt"), []byte("203.0.113.10"), 0600)
	f := NewFirewall(root, 701)
	f.Platform = "linux"
	f.Path = "synthetic"
	added := 0
	f.Execute = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if args[2] == "-C" {
			return nil, ErrRuleMissing
		}
		added++
		if added > 1 {
			return nil, errors.New("simulated failure")
		}
		return nil, nil
	}
	if _, err := f.Apply(context.Background(), "add", ""); err == nil || err.Error() != "firewall_partial_failure" {
		t.Fatal("partial failure lost", err)
	}
	f.Platform = "darwin"
	if _, err := f.Apply(context.Background(), "remove", ""); err == nil {
		t.Fatal("Linux firewall called on Mac")
	}
}
