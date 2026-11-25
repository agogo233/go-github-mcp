package transport

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	mcpserver "github.com/mark3labs/mcp-go/server"

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
	handler := authResolve(next, cfg, logins, "", false)

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
		// The default PAT's verified login must be attributed in the
		// request context so audit logs can name the account used.
		if l, _ := r.Context().Value(ghLoginKey{}).(string); l != "default-login" {
			w.WriteHeader(http.StatusExpectationFailed)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	handler := authResolve(next, cfg, nil, "default-login", true)

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
	handler := authResolve(next, cfg, nil, "", false)

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
	handler := authResolve(next, cfg, logins, "", false)

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

func TestBearerAuth(t *testing.T) {
	cfg := &config.Config{SharedSecret: "shared-secret"}
	handler := bearerAuth(okHandler(), cfg)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/mcp", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("missing header: status = %d, want 401", rec.Code)
	}
	if rec.Header().Get("WWW-Authenticate") == "" {
		t.Error("401 must carry WWW-Authenticate")
	}

	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set("Authorization", "Bearer wrong-secret")
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("wrong secret: status = %d, want 401", rec.Code)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set("Authorization", "Bearer shared-secret")
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("correct secret: status = %d, want 200", rec.Code)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set("Authorization", "bearer shared-secret")
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("case-insensitive scheme: status = %d, want 200", rec.Code)
	}
}

func TestProbeCallInfoExtractsToolAndOwner(t *testing.T) {
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get_issue","arguments":{"owner":"Alice","repo":"demo","number":1}}}`
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	ci := probeCallInfo(req)
	if ci.tool != "get_issue" {
		t.Errorf("probed tool = %q, want get_issue", ci.tool)
	}
	if ci.owner != "Alice" {
		t.Errorf("probed owner = %q, want Alice", ci.owner)
	}

	got, _ := io.ReadAll(req.Body)
	if string(got) != body {
		t.Errorf("request body not preserved for the next handler: %q", string(got))
	}
}

func TestProbeCallInfoNonToolCallHasNoOwner(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"name":"server"}}`))
	if ci := probeCallInfo(req); ci.tool != "" || ci.owner != "" {
		t.Errorf("initialize probe = %+v, want empty callInfo", ci)
	}
}

// A confirmed tools/call whose arguments continue past the probe window must
// keep the owner captured before the window ends — dropping it would silently
// reroute the call to the random fallback pool.
func TestProbeCallInfoKeepsOwnerWhenArgumentsExceedWindow(t *testing.T) {
	pad := strings.Repeat("x", 2*toolNameProbeLimit)
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"push_files","arguments":{"owner":"alice","content":"` + pad + `"}}}`
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	ci := probeCallInfo(req)
	if ci.tool != "push_files" {
		t.Errorf("probed tool = %q, want push_files", ci.tool)
	}
	if ci.owner != "alice" {
		t.Errorf("probed owner = %q, want alice (must survive window exhaustion)", ci.owner)
	}

	got, _ := io.ReadAll(req.Body)
	if string(got) != body {
		t.Errorf("request body not preserved for the next handler: %q", string(got))
	}
}

// A non-string owner value must not derail the scan: the value is consumed
// and the owner simply stays empty (routing then uses the random pool).
func TestProbeCallInfoToleratesNonStringOwner(t *testing.T) {
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get_issue","arguments":{"owner":{"nested":"x"},"repo":"demo"}}}`
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	ci := probeCallInfo(req)
	if ci.tool != "get_issue" {
		t.Errorf("probed tool = %q, want get_issue", ci.tool)
	}
	if ci.owner != "" {
		t.Errorf("probed owner = %q, want empty for a non-string value", ci.owner)
	}
	got, _ := io.ReadAll(req.Body)
	if string(got) != body {
		t.Errorf("request body not preserved for the next handler: %q", string(got))
	}
}

// ownerRoute must bind the PAT selected by the (case-insensitive) owner
// argument, the verified login, and the routing decision into the context.
func TestOwnerRouteRoutesByOwner(t *testing.T) {
	cfg := &config.Config{
		GitHubToken: "default-pat",
		OwnerTokens: []config.AuthEntry{
			{Key: "alice", PAT: "pat_alice"},
			{Key: "myorg", PAT: "pat_org"},
		},
	}
	patLogins := map[string]string{"pat_alice": "alice", "pat_org": "myorg"}
	var tok, routedBy, owner, login string
	next := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		tok = githubclient.TokenFromContext(r.Context())
		routedBy, _ = r.Context().Value(routedByKey{}).(string)
		owner, _ = r.Context().Value(ownerKey{}).(string)
		login, _ = r.Context().Value(ghLoginKey{}).(string)
	})
	handler := ownerRoute(next, cfg, patLogins)

	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get_issue","arguments":{"owner":"Alice","repo":"demo"}}}`
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	handler.ServeHTTP(httptest.NewRecorder(), req)

	if tok != "pat_alice" {
		t.Errorf("resolved token = %q, want pat_alice", tok)
	}
	if routedBy != config.RouteOwner {
		t.Errorf("routed_by = %q, want %q", routedBy, config.RouteOwner)
	}
	if owner != "Alice" {
		t.Errorf("owner in ctx = %q, want Alice", owner)
	}
	if login != "alice" {
		t.Errorf("gh_login in ctx = %q, want alice", login)
	}
}

// Unknown owners — and tools without an owner argument — must fall back to a
// random pick from the routing table so GitHub rate limits stay spread.
func TestOwnerRouteFallsBackToRandomPool(t *testing.T) {
	cfg := &config.Config{
		GitHubToken: "default-pat",
		OwnerTokens: []config.AuthEntry{
			{Key: "alice", PAT: "pat_alice"},
			{Key: "myorg", PAT: "pat_org"},
		},
	}
	pool := map[string]bool{"pat_alice": true, "pat_org": true}
	var tok, routedBy string
	next := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		tok = githubclient.TokenFromContext(r.Context())
		routedBy, _ = r.Context().Value(routedByKey{}).(string)
	})
	handler := ownerRoute(next, cfg, nil)

	bodies := []string{
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get_issue","arguments":{"owner":"nobody","repo":"demo"}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"get_me","arguments":{}}}`,
	}
	for _, body := range bodies {
		req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
		handler.ServeHTTP(httptest.NewRecorder(), req)
		if !pool[tok] {
			t.Errorf("fallback token = %q, want one of the routing table", tok)
		}
		if routedBy != config.RouteRandom {
			t.Errorf("fallback routed_by = %q, want %q", routedBy, config.RouteRandom)
		}
	}
}

// An empty routing table (ALLOW_UNAUTHENTICATED debugging without owner
// entries) must use the default PAT and say so in routed_by.
func TestOwnerRouteEmptyTableUsesDefaultPAT(t *testing.T) {
	cfg := &config.Config{GitHubToken: "default-pat"}
	var tok, routedBy string
	next := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		tok = githubclient.TokenFromContext(r.Context())
		routedBy, _ = r.Context().Value(routedByKey{}).(string)
	})
	handler := ownerRoute(next, cfg, nil)

	req := httptest.NewRequest(http.MethodPost, "/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get_me","arguments":{}}}`))
	handler.ServeHTTP(httptest.NewRecorder(), req)

	if tok != "default-pat" {
		t.Errorf("token = %q, want default-pat", tok)
	}
	if routedBy != config.RouteDefault {
		t.Errorf("routed_by = %q, want %q", routedBy, config.RouteDefault)
	}
}

func TestAuditLogRecordsRoutingFields(t *testing.T) {
	var buf strings.Builder
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	ctx := context.Background()
	ctx = context.WithValue(ctx, callInfoKey{}, callInfo{tool: "get_issue", owner: "Alice"})
	ctx = context.WithValue(ctx, routedByKey{}, config.RouteOwner)
	ctx = context.WithValue(ctx, ownerKey{}, "Alice")
	ctx = context.WithValue(ctx, ghLoginKey{}, "alice")
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil).WithContext(ctx)
	auditLog(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})).ServeHTTP(httptest.NewRecorder(), req)

	out := buf.String()
	for _, want := range []string{"tool=get_issue", "routed_by=owner", "owner=Alice", "gh_login=alice"} {
		if !strings.Contains(out, want) {
			t.Errorf("audit log missing %q, got: %s", want, out)
		}
	}
}

// End-to-end owner-mode chain: bearerAuth → ownerRoute → auditLog. A valid
// shared secret plus an owner argument must resolve the right PAT, attribute
// the login, and leave the body intact for the transport.
func TestOwnerModeChainEndToEnd(t *testing.T) {
	var buf strings.Builder
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	cfg := &config.Config{
		GitHubToken:  "default-pat",
		SharedSecret: "shared-secret",
		OwnerTokens:  []config.AuthEntry{{Key: "alice", PAT: "pat_alice"}},
	}
	patLogins := map[string]string{"pat_alice": "alice"}
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get_issue","arguments":{"owner":"alice","repo":"demo"}}}`
	var gotBody string
	next := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
	})
	handler := bearerAuth(ownerRoute(auditLog(next), cfg, patLogins), cfg)

	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer shared-secret")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if gotBody != body {
		t.Errorf("request body not preserved: %q", gotBody)
	}
	out := buf.String()
	for _, want := range []string{"routed_by=owner", "owner=alice", "gh_login=alice"} {
		if !strings.Contains(out, want) {
			t.Errorf("audit log missing %q, got: %s", want, out)
		}
	}

	// A wrong secret is rejected before the body is buffered or routed.
	buf.Reset()
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer nope")
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("wrong secret: status = %d, want 401", rec.Code)
	}
	if buf.String() != "" {
		t.Errorf("rejected requests must not be audited, got: %s", buf.String())
	}
}

func TestAuthFailGuardBlocksRepeatedFailures(t *testing.T) {
	discardLogs(t)
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if bearerToken(r) == nil {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	handler := authFailGuard(next)
	req := func(ip string) *http.Request {
		r := httptest.NewRequest(http.MethodPost, "/mcp", nil)
		r.RemoteAddr = ip + ":1234"
		return r
	}
	for i := 0; i < authFailThreshold; i++ {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req("10.0.0.1"))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: status = %d, want 401", i+1, rec.Code)
		}
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req("10.0.0.1"))
	if rec.Code != http.StatusTooManyRequests {
		t.Errorf("status after %d failures = %d, want 429", authFailThreshold, rec.Code)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Error("429 must carry Retry-After")
	}
	// A different source address is not blocked by 10.0.0.1's failures.
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req("10.0.0.2"))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("other IP status = %d, want 401 (not blocked)", rec.Code)
	}
}

func TestAuthFailGuardSuccessDecaysCounter(t *testing.T) {
	discardLogs(t)
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if bearerToken(r) == nil {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	handler := authFailGuard(next)
	mk := func(withToken bool) *http.Request {
		r := httptest.NewRequest(http.MethodPost, "/mcp", nil)
		r.RemoteAddr = "10.1.1.1:9999"
		if withToken {
			r.Header.Set("Authorization", "Bearer good")
		}
		return r
	}
	for i := 0; i < authFailThreshold-1; i++ {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, mk(false))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: status = %d, want 401", i+1, rec.Code)
		}
	}
	// One success halves the counter (19 -> 9) instead of clearing it, so a
	// sustained guessing campaign cannot be washed out by periodic successes
	// made with another valid key on a multi-identity deployment.
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, mk(true))
	if rec.Code != http.StatusOK {
		t.Fatalf("success status = %d, want 200", rec.Code)
	}
	// Ten more failures keep the counter below the threshold (9 + 10 = 19).
	for i := 0; i < 10; i++ {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, mk(false))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("post-decay attempt %d: status = %d, want 401", i+1, rec.Code)
		}
	}
	// The threshold-crossing failure is answered by auth with 401; only
	// subsequent requests are blocked.
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, mk(false))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("threshold-crossing request: status = %d, want 401", rec.Code)
	}
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, mk(false))
	if rec.Code != http.StatusTooManyRequests {
		t.Errorf("request past threshold: status = %d, want 429", rec.Code)
	}
}

// Flooding the failure map from many rotating sources must not lift an
// already-active block: the reset keeps entries whose block window is open.
func TestAuthFailGuardResetKeepsActiveBlocks(t *testing.T) {
	discardLogs(t)
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if bearerToken(r) == nil {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	handler := authFailGuard(next)
	req := func(ip string) *http.Request {
		r := httptest.NewRequest(http.MethodPost, "/mcp", nil)
		r.RemoteAddr = ip + ":1234"
		return r
	}
	// Block 10.0.0.1 outright.
	for i := 0; i < authFailThreshold; i++ {
		handler.ServeHTTP(httptest.NewRecorder(), req("10.0.0.1"))
	}
	// Flood from enough other sources to trip the map-size reset. The
	// blocked source is skipped by the guard, so it does not count here.
	for i := 0; i < authFailMaxEntries; i++ {
		ip := fmt.Sprintf("10.%d.%d.%d", (i>>16)&0xff, (i>>8)&0xff, i&0xff)
		if ip == "10.0.0.1" {
			continue
		}
		handler.ServeHTTP(httptest.NewRecorder(), req(ip))
	}
	// The active block must have survived the reset.
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req("10.0.0.1"))
	if rec.Code != http.StatusTooManyRequests {
		t.Errorf("blocked source after reset: status = %d, want 429", rec.Code)
	}
}

func TestAuthFailGuardPassesSuccessThrough(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	})
	handler := authFailGuard(next)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/mcp", nil))
	if rec.Code != http.StatusTeapot {
		t.Errorf("status = %d, want %d (guard must not interfere)", rec.Code, http.StatusTeapot)
	}
}

// Regression: the Streamable HTTP transport must stay on the legacy protocol
// negotiation path. go-sdk clients on the stateless 2026-07-28 core emit
// notifications/cancelled without the modern _meta protocolVersion stamp;
// under modern-era validation the server answers 400 and the client
// connection is permanently poisoned (every later call fails with "client is
// closing"). Pinned to 2025-11-25, notifications are accepted and
// modern-era requests are refused with the supported version list so clients
// negotiate down to the legacy path.
func TestStreamableHandlerPinsLegacyProtocol(t *testing.T) {
	discardLogs(t)
	srv := mcpserver.NewMCPServer("test", "1.0.0")
	ts := httptest.NewServer(streamableHandler(srv))
	defer ts.Close()

	post := func(body string, extra map[string]string) *http.Response {
		req, err := http.NewRequest(http.MethodPost, ts.URL+endpointPath, strings.NewReader(body))
		if err != nil {
			t.Fatalf("new request: %v", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		for k, v := range extra {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("POST: %v", err)
		}
		t.Cleanup(func() { resp.Body.Close() })
		return resp
	}

	// Initialize on the latest legacy revision to obtain a session.
	initBody := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"test","version":"1.0.0"}}}`
	resp := post(initBody, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("initialize status = %d, want 200", resp.StatusCode)
	}
	sessionID := resp.Header.Get("Mcp-Session-Id")
	if sessionID == "" {
		t.Fatal("initialize response missing Mcp-Session-Id")
	}

	// A legacy-era cancellation notification must be accepted without any
	// _meta protocolVersion stamp — this is the failure mode that poisoned
	// go-sdk client connections under modern-era validation.
	notify := `{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":"1","reason":"test"}}`
	resp = post(notify, map[string]string{
		"Mcp-Session-Id":       sessionID,
		"MCP-Protocol-Version": "2025-11-25",
	})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("legacy notification status = %d, want 202", resp.StatusCode)
	}

	// A modern-era request must be refused with the supported version list so
	// clients negotiate down instead of failing later on stamped-free
	// notifications.
	modern := `{"jsonrpc":"2.0","id":2,"method":"ping","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28"}}}`
	resp = post(modern, map[string]string{
		"Mcp-Session-Id":       sessionID,
		"MCP-Protocol-Version": "2026-07-28",
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("modern request status = %d, want 400", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `"supported":["2025-11-25"]`) {
		t.Errorf("modern rejection body = %s, want supported=[2025-11-25]", body)
	}
}
