// Package gateway implements the loopback control-plane proxy and its legacy policy contract.
package gateway

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"regexp"
	"slices"
	"strings"
)

const MaxBody = 1024 * 1024

var staticPathRE = regexp.MustCompile(`^/[A-Za-z0-9/_-]*$`)
var fieldRE = regexp.MustCompile(`^[A-Za-z0-9_-]{1,80}$`)
var methods = []string{"GET", "HEAD", "POST", "PUT", "PATCH", "DELETE"}

type Rule struct {
	Method  string   `json:"method"`
	Path    string   `json:"path"`
	Query   []string `json:"query"`
	Headers []string `json:"headers"`
	MaxBody int      `json:"max_body"`
}
type Policy struct {
	Mode        string   `json:"mode"`
	TokenPolicy string   `json:"token_policy"`
	Sink        []string `json:"sink_post_prefixes"`
	Allow       []string `json:"token_allow_prefixes"`
	Rules       []Rule   `json:"rules"`
	Audit       []string `json:"audit_paths"`
}

func DefaultPolicy() Policy {
	return Policy{Mode: "observe", TokenPolicy: "balanced", Sink: []string{}, Allow: []string{}, Rules: []Rule{}, Audit: []string{}}
}
func staticPath(path string) bool {
	return len(path) <= 2048 && staticPathRE.MatchString(path) && !strings.Contains(path, "//")
}
func DecodePolicy(raw []byte) (Policy, error) {
	p := DefaultPolicy()
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&p); err != nil || decoder.Decode(new(any)) != io.EOF {
		return p, errors.New("invalid_gateway_config")
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil || fields["mode"] == nil {
		return p, errors.New("invalid_gateway_config")
	}
	for _, key := range []string{"mode", "token_policy"} {
		if bytes.Equal(bytes.TrimSpace(fields[key]), []byte("null")) {
			return p, errors.New("invalid_gateway_config")
		}
	}
	var rows []map[string]json.RawMessage
	if field, ok := fields["rules"]; ok {
		if json.Unmarshal(field, &rows) != nil {
			return p, errors.New("invalid_gateway_rule")
		}
		for _, row := range rows {
			if len(row) != 5 || row["method"] == nil || row["path"] == nil || row["query"] == nil || row["headers"] == nil || row["max_body"] == nil || bytes.Equal(bytes.TrimSpace(row["max_body"]), []byte("null")) {
				return p, errors.New("invalid_gateway_rule")
			}
		}
	}
	return p, p.Validate()
}
func (p Policy) Validate() error {
	if !slices.Contains([]string{"observe", "enforce", "strict"}, p.Mode) || p.TokenPolicy != "balanced" {
		return errors.New("invalid_gateway_mode")
	}
	for name, paths := range map[string][]string{"sink": p.Sink, "allow": p.Allow, "audit": p.Audit} {
		if paths == nil || len(paths) > 200 {
			return errors.New("invalid_gateway_paths")
		}
		for _, path := range paths {
			if !staticPath(path) {
				return errors.New("invalid_gateway_paths")
			}
			if name != "audit" && path == "/" {
				return errors.New("gateway_root_prefix_rejected")
			}
		}
	}
	if p.Rules == nil || len(p.Rules) > 200 {
		return errors.New("invalid_gateway_rules")
	}
	seen := map[string]bool{}
	for _, rule := range p.Rules {
		if !slices.Contains(methods, rule.Method) || !staticPath(rule.Path) {
			return errors.New("invalid_gateway_rule_path")
		}
		key := rule.Method + " " + rule.Path
		if seen[key] {
			return errors.New("duplicate_gateway_rule")
		}
		seen[key] = true
		for _, values := range [][]string{rule.Query, rule.Headers} {
			if values == nil || len(values) > 100 {
				return errors.New("invalid_gateway_rule_fields")
			}
			for _, field := range values {
				if !fieldRE.MatchString(field) {
					return errors.New("invalid_gateway_rule_fields")
				}
			}
		}
		if rule.MaxBody < 0 || rule.MaxBody > MaxBody {
			return errors.New("invalid_gateway_body_limit")
		}
	}
	return nil
}
func (p Policy) Hash() string {
	// 用排序后的 JSON 对象保持 Python policy_hash 的持久化格式。
	raw, _ := json.Marshal(p)
	var canonical map[string]any
	json.Unmarshal(raw, &canonical)
	raw, _ = json.Marshal(canonical)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func (p Policy) Decision(method, target string, size int64) (string, *Rule) {
	if !strings.HasPrefix(target, "/") || strings.HasPrefix(target, "//") {
		return "deny", nil
	}
	parsed, err := url.Parse(target)
	if err != nil || parsed.Scheme != "" || parsed.Host != "" || strings.Contains(target, "#") {
		return "deny", nil
	}
	rawPath, _, _ := strings.Cut(target, "?")
	if strings.ContainsAny(rawPath, "%\\") {
		return "deny", nil
	}
	for _, part := range strings.Split(rawPath, "/") {
		if part == "." || part == ".." {
			return "deny", nil
		}
	}
	if p.Mode == "strict" {
		for _, rule := range p.Rules {
			if rule.Method == method && rule.Path == parsed.Path {
				if size > int64(rule.MaxBody) || size < 0 || strings.Count(parsed.RawQuery, "&") >= 100 {
					return "deny", nil
				}
				query, err := url.ParseQuery(parsed.RawQuery)
				if err != nil {
					return "deny", nil
				}
				for key := range query {
					if !slices.Contains(rule.Query, key) {
						return "deny", nil
					}
				}
				return "forward", &rule
			}
		}
		return "deny", nil
	}
	matches := func(prefix string) bool {
		return parsed.Path == prefix || strings.HasPrefix(parsed.Path, strings.TrimSuffix(prefix, "/")+"/")
	}
	if p.Mode == "enforce" && slices.Contains([]string{"POST", "PUT", "PATCH", "DELETE"}, method) {
		if slices.ContainsFunc(p.Sink, matches) && !slices.ContainsFunc(p.Allow, matches) {
			return "sink", nil
		}
	}
	return "forward", nil
}
func ApprovedUpstream(value string) bool {
	return value == "openapi.qoder.com.cn" || value == "openapi.qoder.sh"
}
