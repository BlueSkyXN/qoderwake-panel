package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

const HealthPath = "/__qwp_gateway_health"

var rewriteFrom = []byte("https://openapi.qoder.com.cn")

type Record struct {
	Time             string `json:"ts"`
	Method           string `json:"method"`
	Path             string `json:"path"`
	Size             int64  `json:"q"`
	Status           int    `json:"status"`
	Received         int64  `json:"r"`
	Action           string `json:"action"`
	ForwardAttempted bool   `json:"forward_attempted"`
	Milliseconds     int64  `json:"ms"`
}
type Handler struct {
	policy    Policy
	upstream  string
	port      int
	health    map[string]any
	audit     func(Record) error
	transport http.RoundTripper
}

func NewHandler(p Policy, upstream string, port int, health map[string]any, audit func(Record) error) (*Handler, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	if !ApprovedUpstream(upstream) {
		return nil, errors.New("invalid_gateway_upstream")
	}
	if port < 1 || port > 65535 {
		return nil, errors.New("invalid_gateway_port")
	}
	// 固定一代的策略，调用者后续修改输入切片不会更改正在服务的规则。
	raw, _ := json.Marshal(p)
	copyPolicy, err := DecodePolicy(raw)
	if err != nil {
		return nil, err
	}
	copyHealth := map[string]any{}
	for key, value := range health {
		copyHealth[key] = value
	}
	copyHealth["configHash"] = copyPolicy.Hash()
	copyHealth["mode"] = copyPolicy.Mode
	copyHealth["port"] = port
	copyHealth["upstream"] = upstream
	transport := &http.Transport{Proxy: nil, DisableCompression: true, DisableKeepAlives: true, ResponseHeaderTimeout: 60 * time.Second, TLSHandshakeTimeout: 15 * time.Second}
	return &Handler{policy: copyPolicy, upstream: upstream, port: port, health: copyHealth, audit: audit, transport: transport}, nil
}
func (h *Handler) Close() {
	if transport, ok := h.transport.(interface{ CloseIdleConnections() }); ok {
		transport.CloseIdleConnections()
	}
}
func reply(w http.ResponseWriter, r *http.Request, status int, code string) {
	var raw []byte
	if status != 204 {
		raw, _ = json.Marshal(map[string]any{"success": false, "error": code})
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", strconv.Itoa(len(raw)))
	w.Header().Set("Connection", "close")
	w.WriteHeader(status)
	if r.Method != "HEAD" {
		w.Write(raw)
	}
}
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.RequestURI == HealthPath && r.Method == "GET" {
		raw, _ := json.Marshal(h.health)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Length", strconv.Itoa(len(raw)))
		w.Header().Set("Connection", "close")
		w.Write(raw)
		return
	}
	start := time.Now()
	record := Record{Time: time.Now().UTC().Format("2006-01-02T15:04:05Z"), Method: r.Method, Path: "/<redacted>", Status: 502, Action: "deny"}
	targetPath, _, _ := strings.Cut(r.RequestURI, "?")
	if slices.Contains(h.policy.Audit, targetPath) || slices.ContainsFunc(h.policy.Rules, func(rule Rule) bool { return rule.Path == targetPath }) {
		record.Path = targetPath
	}
	defer func() {
		record.Milliseconds = time.Since(start).Milliseconds()
		if h.audit != nil {
			_ = h.audit(record)
		}
	}()
	deny := func(status int, code string) { record.Status = status; reply(w, r, status, code) }
	if !slices.Contains(methods, r.Method) {
		deny(405, "unsupported_method")
		return
	}
	if len(r.TransferEncoding) > 0 || len(r.Header.Values("Content-Length")) > 1 || r.Header.Get("Transfer-Encoding") != "" || r.ContentLength < 0 {
		deny(400, "unsupported_framing")
		return
	}
	record.Size = r.ContentLength
	if record.Size > MaxBody {
		deny(413, "body_too_large")
		return
	}
	action, rule := h.policy.Decision(r.Method, r.RequestURI, record.Size)
	record.Action = action
	if action == "deny" {
		deny(403, "gateway_policy_denied")
		return
	}
	if action == "sink" {
		deny(204, "")
		return
	}
	var body []byte
	if record.Size > 0 {
		if r.Body == nil {
			deny(400, "incomplete_body")
			return
		}
		var err error
		body, err = io.ReadAll(io.LimitReader(r.Body, record.Size+1))
		if err != nil || int64(len(body)) != record.Size {
			deny(400, "incomplete_body")
			return
		}
	}
	parsed, err := url.Parse("https://" + h.upstream + r.RequestURI)
	if err != nil || parsed.Host != h.upstream || parsed.Scheme != "https" {
		deny(403, "gateway_policy_denied")
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	idle := time.AfterFunc(60*time.Second, cancel)
	defer idle.Stop()
	request, err := http.NewRequestWithContext(ctx, r.Method, parsed.String(), bytes.NewReader(body))
	if err != nil {
		deny(400, "invalid_request")
		return
	}
	hop := map[string]bool{}
	for _, key := range []string{"host", "connection", "content-length", "accept-encoding", "transfer-encoding", "proxy-authorization", "proxy-authenticate", "keep-alive", "te", "trailer", "upgrade"} {
		hop[key] = true
	}
	for _, key := range strings.Split(r.Header.Get("Connection"), ",") {
		hop[strings.ToLower(strings.TrimSpace(key))] = true
	}
	allowed := map[string]bool{}
	if rule != nil {
		for _, key := range rule.Headers {
			allowed[strings.ToLower(key)] = true
		}
	}
	for key, values := range r.Header {
		if !hop[strings.ToLower(key)] && (rule == nil || allowed[strings.ToLower(key)]) {
			request.Header[key] = append([]string(nil), values...)
		}
	}
	request.Host = h.upstream
	request.Close = true
	request.Header.Set("Accept-Encoding", "identity")
	if request.Header.Get("User-Agent") == "" {
		request.Header["User-Agent"] = []string{""}
	}
	record.ForwardAttempted = true
	response, err := h.transport.RoundTrip(request)
	if err != nil {
		deny(502, "gateway_upstream_unavailable")
		return
	}
	defer response.Body.Close()
	idle.Reset(60 * time.Second)
	record.Status = response.StatusCode
	for _, name := range []string{"Content-Type", "Cache-Control", "Etag"} {
		for _, value := range response.Header.Values(name) {
			w.Header().Add(name, value)
		}
	}
	w.Header().Set("Connection", "close")
	w.WriteHeader(response.StatusCode)
	if r.Method == "HEAD" {
		return
	}
	reader := &idleReader{response.Body, func() { idle.Reset(60 * time.Second) }}
	write := func(raw []byte) error {
		if e := http.NewResponseController(w).SetWriteDeadline(time.Now().Add(60 * time.Second)); e != nil && !errors.Is(e, http.ErrNotSupported) {
			return e
		}
		n, e := w.Write(raw)
		if e == nil && n != len(raw) {
			e = io.ErrShortWrite
		}
		return e
	}
	if strings.Contains(strings.ToLower(response.Header.Get("Content-Type")), "event-stream") {
		_ = http.NewResponseController(w).Flush()
		buffer := make([]byte, 8192)
		for {
			n, e := reader.Read(buffer)
			if n > 0 {
				if err = write(buffer[:n]); err != nil {
					record.Status = 502
					return
				}
				record.Received += int64(n)
				_ = http.NewResponseController(w).Flush()
			}
			if e == io.EOF {
				return
			}
			if e != nil {
				record.Status = 502
				return
			}
		}
	}
	received, err := copyRewritten(reader, write, []byte(fmt.Sprintf("http://qwgw.local.test:%d", h.port)))
	record.Received = received
	if err != nil {
		record.Status = 502
	}
}

type idleReader struct {
	io.Reader
	touch func()
}

func (r *idleReader) Read(raw []byte) (int, error) {
	n, e := r.Reader.Read(raw)
	if n > 0 {
		r.touch()
	}
	return n, e
}
func copyRewritten(reader io.Reader, write func([]byte) error, replacement []byte) (int64, error) {
	var received int64
	pending := []byte{}
	buffer := make([]byte, 8192)
	for {
		n, e := reader.Read(buffer)
		pending = append(pending, buffer[:n]...)
		if len(pending) >= len(rewriteFrom) {
			end := len(pending) - len(rewriteFrom) + 1
			from := max(0, end-len(rewriteFrom)+1)
			match := bytes.Index(pending[from:], rewriteFrom)
			if match >= 0 {
				match += from
				if match < end && match+len(rewriteFrom) > end {
					end = match
				}
			}
			if end > 0 {
				if err := write(bytes.ReplaceAll(pending[:end], rewriteFrom, replacement)); err != nil {
					return received, err
				}
				received += int64(end)
				pending = append([]byte(nil), pending[end:]...)
			}
		}
		if e == io.EOF {
			if err := write(bytes.ReplaceAll(pending, rewriteFrom, replacement)); err != nil {
				return received, err
			}
			return received + int64(len(pending)), nil
		}
		if e != nil {
			return received, e
		}
	}
}
