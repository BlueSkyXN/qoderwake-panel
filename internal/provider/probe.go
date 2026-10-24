package provider

import (
	"context"
	"crypto/subtle"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"qoderwake-panel/internal/apierror"
	"qoderwake-panel/internal/auth"
)

func ValidateURL(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || utf8.RuneCountInString(value) > 2048 {
		return "", apierror.Invalid("invalid_provider_url")
	}
	parsed, err := url.Parse(value)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", apierror.Invalid("invalid_provider_url")
	}
	if parsed.Port() != "" {
		port, e := strconv.Atoi(parsed.Port())
		if e != nil || port < 1 || port > 65535 {
			return "", apierror.Invalid("invalid_provider_url")
		}
	}
	if parsed.Scheme != "https" && !explicitLoopback(parsed.Hostname()) {
		return "", apierror.Invalid("provider_requires_https")
	}
	if ip, e := netip.ParseAddr(parsed.Hostname()); e == nil && (ip.IsLinkLocalUnicast() || ip.IsUnspecified() || ip.IsMulticast()) {
		return "", apierror.Invalid("provider_destination_rejected")
	}
	return strings.TrimRight(value, "/"), nil
}
func explicitLoopback(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

type Plan struct {
	ID                                     string  `json:"id"`
	Mode                                   string  `json:"mode"`
	Name                                   string  `json:"name"`
	Target                                 string  `json:"target"`
	Expires                                float64 `json:"expires"`
	principal, revision, url, secretDigest string
}
type Plans struct {
	mu    sync.Mutex
	items map[string]Plan
	now   func() time.Time
}

func NewPlans() *Plans { return &Plans{items: map[string]Plan{}, now: time.Now} }
func (p *Plans) Create(principal, mode, name, revision, address, key string) (Plan, error) {
	normalized, err := ValidateURL(address)
	if err != nil {
		return Plan{}, err
	}
	parsed, _ := url.Parse(normalized)
	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/models"
	parsed.RawPath = ""
	id, err := auth.Random(32)
	if err != nil {
		return Plan{}, err
	}
	plan := Plan{ID: id, Mode: mode, Name: name, Target: parsed.String(), Expires: float64(p.now().Unix()) + 300, principal: principal, revision: revision, url: normalized}
	if mode == "adhoc" {
		plan.secretDigest = auth.Digest(key)
	} else if mode != "saved" {
		return Plan{}, apierror.Invalid("invalid_provider_probe_mode")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for id, v := range p.items {
		if v.Expires <= float64(p.now().Unix()) {
			delete(p.items, id)
		}
	}
	p.items[id] = plan
	return plan, nil
}
func equal(a, b string) bool { return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1 }
func (p *Plans) Consume(id, principal, mode, name, revision, address, key string) (Plan, error) {
	p.mu.Lock()
	plan, ok := p.items[id]
	if !ok || plan.Expires <= float64(p.now().Unix()) || !equal(plan.principal, principal) {
		p.mu.Unlock()
		return Plan{}, apierror.Invalid("invalid_provider_probe_plan")
	}
	delete(p.items, id)
	p.mu.Unlock()
	valid := plan.Mode == mode && equal(plan.url, address)
	if mode == "saved" {
		valid = valid && equal(plan.Name, name) && equal(plan.revision, revision)
	} else if mode == "adhoc" {
		valid = valid && equal(plan.secretDigest, auth.Digest(key))
	} else {
		valid = false
	}
	if !valid {
		return Plan{}, apierror.Invalid("invalid_provider_probe_plan")
	}
	return plan, nil
}

var reserved = []netip.Prefix{netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("100.64.0.0/10"), netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("192.0.2.0/24"), netip.MustParsePrefix("198.18.0.0/15"), netip.MustParsePrefix("198.51.100.0/24"), netip.MustParsePrefix("203.0.113.0/24"), netip.MustParsePrefix("240.0.0.0/4"), netip.MustParsePrefix("100::/64"), netip.MustParsePrefix("2001:db8::/32"), netip.MustParsePrefix("2001::/23"), netip.MustParsePrefix("2002::/16")}

func AllowedAddresses(host string, addresses []netip.Addr) error {
	if len(addresses) == 0 {
		return apierror.Invalid("provider_dns_empty")
	}
	for _, raw := range addresses {
		ip := raw.Unmap()
		allowed := ip.IsGlobalUnicast() && !ip.IsPrivate() && !ip.IsLoopback() && !ip.IsLinkLocalUnicast()
		for _, prefix := range reserved {
			if prefix.Contains(ip) {
				allowed = false
			}
		}
		if explicitLoopback(host) {
			allowed = ip.IsLoopback()
		}
		if !allowed {
			return apierror.Invalid("provider_private_destination_rejected")
		}
	}
	return nil
}
func Probe(ctx context.Context, address, key string) (bool, string, error) {
	normalized, err := ValidateURL(address)
	if err != nil {
		return false, "", err
	}
	if key == "" || utf8.RuneCountInString(key) > 2048 || strings.ContainsAny(key, "\r\n") {
		return false, "", apierror.Invalid("invalid_provider_key")
	}
	parsed, _ := url.Parse(normalized)
	port := parsed.Port()
	if port == "" {
		port = "443"
		if parsed.Scheme == "http" {
			port = "80"
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	addresses, err := net.DefaultResolver.LookupNetIP(ctx, "ip", parsed.Hostname())
	if err != nil {
		return false, "", apierror.Invalid("provider_dns_empty")
	}
	if err = AllowedAddresses(parsed.Hostname(), addresses); err != nil {
		return false, "", err
	}
	host := parsed.Hostname()
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true, TLSClientConfig: &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}, DialContext: func(ctx context.Context, network, target string) (net.Conn, error) {
		var last error
		for _, address := range addresses {
			connection, e := (&net.Dialer{}).DialContext(ctx, "tcp", net.JoinHostPort(address.String(), port))
			if e == nil {
				return connection, nil
			}
			last = e
		}
		return nil, last
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/models"
	parsed.RawPath = ""
	request, err := http.NewRequestWithContext(ctx, "GET", parsed.String(), nil)
	if err != nil {
		return false, "", err
	}
	request.Header.Set("Authorization", "Bearer "+key)
	request.Header.Set("Accept", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return false, "无法连接 Provider，请检查地址、网络与证书。", nil
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return false, fmt.Sprintf("HTTP %d（重定向不跟随）", response.StatusCode), nil
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, 1024*1024+1))
	if err != nil {
		return false, "", err
	}
	if len(raw) > 1024*1024 {
		return false, "", apierror.Invalid("provider_response_too_large")
	}
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return false, "", apierror.Invalid("provider_invalid_response")
	}
	if object, ok := value.(map[string]any); ok {
		value = object["data"]
	}
	items, ok := value.([]any)
	if !ok {
		return false, "", apierror.Invalid("provider_invalid_response")
	}
	names := []string{}
	for _, item := range items[:min(8, len(items))] {
		if m, ok := item.(map[string]any); ok {
			label := fmt.Sprint(m["id"])
			runes := []rune(label)
			names = append(names, string(runes[:min(200, len(runes))]))
		}
	}
	return true, "HTTP 200 · 可用模型: " + strings.Join(names, ", "), nil
}
