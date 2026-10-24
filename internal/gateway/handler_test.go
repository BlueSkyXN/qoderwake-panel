package gateway

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func handler(t *testing.T, raw string) (*Handler, *[]Record) {
	t.Helper()
	p, err := DecodePolicy([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	rows := []Record{}
	h, err := NewHandler(p, "openapi.qoder.sh", 19841, map[string]any{"service": "qw-control-gateway"}, func(row Record) error { rows = append(rows, row); return nil })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.Close)
	return h, &rows
}
func TestDeniedAndSinkNeverForwardOrLogPrivateMaterial(t *testing.T) {
	for _, mode := range []string{"strict", "enforce"} {
		h, rows := handler(t, `{"mode":"`+mode+`","sink_post_prefixes":["/tracking"]}`)
		h.transport = transportFunc(func(*http.Request) (*http.Response, error) { t.Fatal("upstream contacted"); return nil, nil })
		r := httptest.NewRequest("POST", "http://localhost/tracking/private-id?secret=value", strings.NewReader("secret-body"))
		r.RequestURI = "/tracking/private-id?secret=value"
		r.Header.Set("Authorization", "Bearer secret-key")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		want := 403
		if mode == "enforce" {
			want = 204
		}
		if w.Code != want {
			t.Fatal(w.Code)
		}
		raw, _ := json.Marshal(rows)
		if strings.Contains(string(raw), "secret") || strings.Contains(string(raw), "private-id") || (*rows)[0].ForwardAttempted {
			t.Fatal("sensitive audit data")
		}
	}
}
func TestForwardOriginHeaderFilterRewriteAndSSE(t *testing.T) {
	h, rows := handler(t, `{"mode":"strict","rules":[{"method":"GET","path":"/catalog","query":[],"headers":["accept"],"max_body":0}]}`)
	payload := append(bytes.Repeat([]byte{'a'}, 8182), rewriteFrom...)
	payload = append(payload, bytes.Repeat([]byte{'z'}, 100)...)
	for _, sse := range []bool{false, true} {
		h.transport = transportFunc(func(r *http.Request) (*http.Response, error) {
			if r.URL.String() != "https://openapi.qoder.sh/catalog" || r.Host != "openapi.qoder.sh" {
				t.Fatal("origin not pinned")
			}
			for _, key := range []string{"Authorization", "Cookie", "Proxy-Authorization", "X-Hop"} {
				if r.Header.Get(key) != "" {
					t.Fatal("header leak", key)
				}
			}
			if r.Header.Get("Accept") != "application/json" || r.Header.Get("Accept-Encoding") != "identity" {
				t.Fatal("expected header missing")
			}
			content := "application/json"
			if sse {
				content = "text/event-stream"
			}
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {content}, "Set-Cookie": {"private"}}, Body: io.NopCloser(bytes.NewReader(payload))}, nil
		})
		r := httptest.NewRequest("GET", "/catalog", nil)
		for _, key := range []string{"Authorization", "Cookie", "Proxy-Authorization", "X-Hop"} {
			r.Header.Set(key, "private")
		}
		r.Header.Set("Accept", "application/json")
		r.Header.Set("Connection", "X-Hop")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		want := payload
		if !sse {
			want = bytes.ReplaceAll(payload, rewriteFrom, []byte("http://qwgw.local.test:19841"))
		}
		if w.Code != 200 || !bytes.Equal(w.Body.Bytes(), want) || w.Header().Get("Set-Cookie") != "" {
			t.Fatal("forward result mismatch")
		}
		if (*rows)[len(*rows)-1].Path != "/catalog" || !(*rows)[len(*rows)-1].ForwardAttempted {
			t.Fatal("forward audit missing")
		}
	}
}

type splitReader struct {
	raw  []byte
	size int
}

func (r *splitReader) Read(b []byte) (int, error) {
	if len(r.raw) == 0 {
		return 0, io.EOF
	}
	n := copy(b, r.raw[:min(r.size, len(r.raw))])
	r.raw = r.raw[n:]
	return n, nil
}
func TestRewriteAcrossEveryChunkBoundary(t *testing.T) {
	raw := []byte("prefix " + string(rewriteFrom) + string(rewriteFrom) + " suffix")
	for size := 1; size < 100; size++ {
		var out bytes.Buffer
		count, err := copyRewritten(&splitReader{raw, size}, func(b []byte) error { _, e := out.Write(b); return e }, []byte("replacement"))
		if err != nil || count != int64(len(raw)) || !bytes.Equal(out.Bytes(), bytes.ReplaceAll(raw, rewriteFrom, []byte("replacement"))) {
			t.Fatal("split rewrite", size, err)
		}
	}
}
func TestMalformedRequestAndHealthAreLocal(t *testing.T) {
	h, _ := handler(t, `{"mode":"observe"}`)
	h.transport = transportFunc(func(*http.Request) (*http.Response, error) { t.Fatal("upstream contacted"); return nil, nil })
	for _, alter := range []func(*http.Request){func(r *http.Request) { r.TransferEncoding = []string{"chunked"} }, func(r *http.Request) { r.ContentLength = MaxBody + 1 }, func(r *http.Request) { r.ContentLength = 12; r.Body = io.NopCloser(strings.NewReader("short")) }, func(r *http.Request) { r.RequestURI = "https://example.invalid/" }} {
		r := httptest.NewRequest("POST", "/", nil)
		alter(r)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code < 400 {
			t.Fatal("invalid request accepted", w.Code)
		}
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", HealthPath, nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"service":"qw-control-gateway"`) {
		t.Fatal("health missing")
	}
}
