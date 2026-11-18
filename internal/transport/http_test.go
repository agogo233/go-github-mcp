package transport

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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

func TestBearerAuth(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := bearerAuth(next, "s3cret")

	tests := []struct {
		name   string
		header string
		want   int
	}{
		{"missing header", "", http.StatusUnauthorized},
		{"wrong token", "Bearer nope", http.StatusUnauthorized},
		{"shorter token", "Bearer s", http.StatusUnauthorized},
		{"longer token", "Bearer s3cret-and-more", http.StatusUnauthorized},
		{"correct token", "Bearer s3cret", http.StatusOK},
		{"case-insensitive scheme", "bearer s3cret", http.StatusOK},
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
		})
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
