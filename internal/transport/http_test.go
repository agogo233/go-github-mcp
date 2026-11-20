package transport

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/agogo233/go-github-mcp/internal/config"
	"github.com/agogo233/go-github-mcp/internal/githubclient"
)

func discardLogs(t *testing.T) {
	t.Helper()
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
}

// Regression: the audit-log wrapper must stay transparent to http.Flusher or
// mcp-go's Streamable HTTP transport rejects SSE ("Streaming unsupported").
func TestAuditLogPreservesStreaming(t *testing.T) {
	discardLogs(t)
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		f, ok := w.(http.Flusher)
		if !ok {
			t.Error("wrapped writer does not implement http.Flusher")
			return
		}
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte("data: hello\n\n"))
		f.Flush()
	})

	rec := httptest.NewRecorder()
	auditLog(handler).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/mcp", nil))

	if !rec.Flushed {
		t.Error("Flush did not propagate to the underlying response writer")
	}
	if rec.Code != http.StatusTeapot {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusTeapot)
	}
	if got := rec.Body.String(); got != "data: hello\n\n" {
		t.Errorf("body = %q", got)
	}
}

func TestStatusWriterCapturesStatus(t *testing.T) {
	rec := httptest.NewRecorder()
	sw := &statusWriter{ResponseWriter: rec, status: http.StatusOK}
	sw.WriteHeader(http.StatusNotFound)
	if sw.status != http.StatusNotFound || rec.Code != http.StatusNotFound {
		t.Errorf("sw.status=%d rec.Code=%d, want both %d", sw.status, rec.Code, http.StatusNotFound)
	}
}

func TestAuthResolveRoutesIdentity(t *testing.T) {
	cfg := &config.Config{
		GitHubToken: "default-pat",
		AuthTokens: []config.AuthEntry{
			{Key: "agent1", PAT: "pat_one"},
			{Key: "agent2", PAT: "pat_two"},
		},
	}
	logins := map[string]string{"agent1": "alice", "agent2": "bob"}
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok := githubclient.TokenFromContext(r.Context())
		if tok == "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(tok))
	})
	handler := authResolve(next, cfg, logins, false)

	tests := []struct {
		name   string
		header string
		want   int
		body   string
	}{
		{"missing header", "", http.StatusUnauthorized, ""},
		{"wrong token", "Bearer nope", http.StatusUnauthorized, ""},
		{"correct agent1", "Bearer agent1", http.StatusOK, "pat_one"},
		{"correct agent2", "Bearer agent2", http.StatusOK, "pat_two"},
		{"case-insensitive scheme", "bearer agent1", http.StatusOK, "pat_one"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
			if tt.header != "" {
				req.Header.Set("Authorization", tt.header)
			}
			handler.ServeHTTP(rec, req)
			if rec.Code != tt.want {
				t.Errorf("status = %d, want %d", rec.Code, tt.want)
			}
			if tt.body != "" && rec.Body.String() != tt.body {
				t.Errorf("body = %q, want %q", rec.Body.String(), tt.body)
			}
		})
	}
}

func TestAuthResolveAnonymousFallback(t *testing.T) {
	cfg := &config.Config{GitHubToken: "default-pat"}
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if tok := githubclient.TokenFromContext(r.Context()); tok != "default-pat" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	handler := authResolve(next, cfg, nil, true)

	for _, h := range []string{"", "Bearer nope"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
		if h != "" {
			req.Header.Set("Authorization", h)
		}
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Errorf("header %q: status = %d, want 200 (anonymous fallback)", h, rec.Code)
		}
	}
}

func TestAuthResolveRejectsWhenAnonymousDisabled(t *testing.T) {
	cfg := &config.Config{GitHubToken: "default-pat", AuthTokens: []config.AuthEntry{{Key: "k", PAT: "p"}}}
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	handler := authResolve(next, cfg, nil, false)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/mcp", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("anonymous request without escape hatch: status = %d, want 401", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "unauthorized") {
		t.Errorf("401 body should be JSON-RPC-shaped, got: %s", rec.Body.String())
	}
}

func TestSanitizeLogField(t *testing.T) {
	tests := []struct {
		in   string
		n    int
		want string
	}{
		{"agent", 128, "agent"},
		{"evil\n2026-01-01 INFO forged line", 128, "evil2026-01-01 INFO forged line"},
		{"tab\tand\rreturns", 128, "tabandreturns"},
		{"del\x7fchar", 128, "delchar"},
		{"utf8-名前-✓", 128, "utf8-名前-✓"},
		{"abcdefgh", 4, "abcd"},
	}
	for _, tt := range tests {
		if got := sanitizeLogField(tt.in, tt.n); got != tt.want {
			t.Errorf("sanitizeLogField(%q, %d) = %q, want %q", tt.in, tt.n, got, tt.want)
		}
	}
}

func TestIsLoopbackAddr(t *testing.T) {
	tests := []struct {
		addr string
		want bool
	}{
		{"127.0.0.1:8080", true},
		{"127.7.7.7:80", true},
		{"localhost:8080", true},
		{"[::1]:8080", true},
		{"0.0.0.0:8080", false},
		{"192.168.1.5:8080", false},
		{"no-addr", false},
	}
	for _, tt := range tests {
		if got := isLoopbackAddr(tt.addr); got != tt.want {
			t.Errorf("isLoopbackAddr(%q) = %v, want %v", tt.addr, got, tt.want)
		}
	}
}

func TestBearerTokenParsing(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer  spaced  ")
	if got := string(bearerToken(req)); got != "spaced" {
		t.Errorf("bearerToken with inner spaces = %q, want %q", got, "spaced")
	}
	req.Header.Set("Authorization", "Basic abc")
	if bearerToken(req) != nil {
		t.Error("non-Bearer scheme must return nil")
	}
	if !strings.Contains(`Bearer realm="go-github-mcp"`, "Bearer") {
		t.Error("sanity")
	}
}

// The audit log should name the invoked MCP tool for tools/call requests
// while leaving the request body intact for the transport.
func TestAuditLogRecordsMCPToolName(t *testing.T) {
	var buf strings.Builder
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"delete_file","arguments":{}}}`
	next := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got, _ := io.ReadAll(r.Body)
		if string(got) != body {
			t.Errorf("request body not preserved for the next handler: %q", string(got))
		}
	})
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	auditLog(next).ServeHTTP(httptest.NewRecorder(), req)

	if !strings.Contains(buf.String(), "tool=delete_file") {
		t.Errorf("audit log missing tool name, got: %s", buf.String())
	}
}

func TestAuditLogOmitsToolNameForNonToolCalls(t *testing.T) {
	var buf strings.Builder
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	req := httptest.NewRequest(http.MethodPost, "/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`))
	auditLog(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})).ServeHTTP(httptest.NewRecorder(), req)

	if strings.Contains(buf.String(), "tool=") {
		t.Errorf("initialize must not be logged as a tool call, got: %s", buf.String())
	}
}

func TestAuditLogRecordsGithubLogin(t *testing.T) {
	var buf strings.Builder
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	ctx := context.WithValue(context.Background(), ghLoginKey{}, "alice")
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil).WithContext(ctx)
	auditLog(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})).ServeHTTP(httptest.NewRecorder(), req)

	if !strings.Contains(buf.String(), "gh_login=alice") {
		t.Errorf("audit log missing gh_login, got: %s", buf.String())
	}
}

// authResolve must bind the verified login into the request context so the
// audit wrapper (which runs inside it) can attribute the call to an account.
func TestAuthResolveWritesLoginToContext(t *testing.T) {
	cfg := &config.Config{AuthTokens: []config.AuthEntry{{Key: "agent1", PAT: "pat_one"}}}
	logins := map[string]string{"agent1": "alice"}
	var gotLogin string
	next := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		if l, ok := r.Context().Value(ghLoginKey{}).(string); ok {
			gotLogin = l
		}
	})
	handler := authResolve(next, cfg, logins, false)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set("Authorization", "Bearer agent1")
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if gotLogin != "alice" {
		t.Errorf("resolved login in ctx = %q, want alice", gotLogin)
	}
}

// A tools/call whose params fall beyond the 64 KiB probe window must still
// have its full body forwarded to the transport; only the tool-name field is
// skipped. This guards the memory-amplification fix: the audit middleware
// must never buffer the whole (up to 10 MiB) body just for a log field.
func TestAuditLogLargeBodyPreservesBodyAndSkipsDistantToolName(t *testing.T) {
	var buf strings.Builder
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	pad := strings.Repeat("x", 2*toolNameProbeLimit)
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","pad":"` + pad +
		`","params":{"name":"delete_file","arguments":{}}}`
	var gotLen int
	next := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got, _ := io.ReadAll(r.Body)
		gotLen = len(got)
	})
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	auditLog(next).ServeHTTP(httptest.NewRecorder(), req)

	if gotLen != len(body) {
		t.Errorf("downstream body length = %d, want %d (body must be preserved)", gotLen, len(body))
	}
	if strings.Contains(buf.String(), "tool=") {
		t.Errorf("tool name beyond the probe window must be skipped, got: %s", buf.String())
	}
}

// JSON-RPC does not mandate member order: params.name must still be picked
// up when it precedes method in the body.
func TestAuditLogRecordsToolNameWhenParamsPrecedeMethod(t *testing.T) {
	var buf strings.Builder
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	body := `{"jsonrpc":"2.0","id":1,"params":{"name":"delete_file","arguments":{}},"method":"tools/call"}`
	var got string
	next := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = string(b)
	})
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	auditLog(next).ServeHTTP(httptest.NewRecorder(), req)

	if !strings.Contains(buf.String(), "tool=delete_file") {
		t.Errorf("audit log missing tool name for params-first body, got: %s", buf.String())
	}
	if got != body {
		t.Errorf("request body not preserved for the next handler: %q", got)
	}
}

// A tools/call larger than the probe window whose tool name sits inside the
// first 64 KiB must still be attributed in the audit log, with the body
// forwarded intact.
func TestAuditLogRecordsToolNameInsideProbeWindowOfLargeBody(t *testing.T) {
	var buf strings.Builder
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	pad := strings.Repeat("x", 2*toolNameProbeLimit)
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"push_files","arguments":{"pad":"` + pad + `"}}}`
	var gotLen int
	next := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got, _ := io.ReadAll(r.Body)
		gotLen = len(got)
	})
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	auditLog(next).ServeHTTP(httptest.NewRecorder(), req)

	if gotLen != len(body) {
		t.Errorf("downstream body length = %d, want %d", gotLen, len(body))
	}
	if !strings.Contains(buf.String(), "tool=push_files") {
		t.Errorf("audit log missing tool name within probe window, got: %s", buf.String())
	}
}

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

func TestRateLimitMiddleware429(t *testing.T) {
	cfg := &config.Config{HTTPRateLimit: 2, HTTPRateBurst: 2}
	handler := rateLimit(okHandler(), cfg)

	// The initial bucket holds burst tokens: the first two requests pass.
	for i := 0; i < 2; i++ {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/mcp", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d: status = %d, want 200 (burst admits it)", i+1, rec.Code)
		}
	}

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/mcp", nil))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("third request: status = %d, want 429", rec.Code)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Error("429 must carry a Retry-After header")
	}
	if !strings.Contains(rec.Body.String(), "rate limited") {
		t.Errorf("body should explain throttling, got: %s", rec.Body.String())
	}
}

func TestConcurrencyLimitMiddleware503(t *testing.T) {
	cfg := &config.Config{HTTPMaxConcurrent: 1}
	started := make(chan struct{})
	release := make(chan struct{})
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		<-release
		w.WriteHeader(http.StatusOK)
	})
	handler := rateLimit(next, cfg)

	firstDone := make(chan struct{})
	go func() {
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/mcp", nil))
		close(firstDone)
	}()
	<-started // first request now occupies the only slot

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/mcp", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("second concurrent request: status = %d, want 503", rec.Code)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Error("503 must carry a Retry-After header")
	}

	close(release)
	<-firstDone
}

func TestRateLimitDisabledPassesThrough(t *testing.T) {
	handler := rateLimit(okHandler(), &config.Config{})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/mcp", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 when limits are disabled", rec.Code)
	}
}

// Long-lived SSE notification streams are GETs and must not occupy
// concurrency slots, or a few idle streams would starve all tool calls.
func TestConcurrencyLimitIgnoresGETStreams(t *testing.T) {
	cfg := &config.Config{HTTPMaxConcurrent: 1}
	started := make(chan struct{})
	release := make(chan struct{})
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			close(started)
			<-release
		}
		w.WriteHeader(http.StatusOK)
	})
	handler := rateLimit(next, cfg)

	firstDone := make(chan struct{})
	go func() {
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/mcp", nil))
		close(firstDone)
	}()
	<-started // POST now occupies the only slot

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/mcp", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET while POST in flight: status = %d, want 200", rec.Code)
	}
	close(release)
	<-firstDone
}
