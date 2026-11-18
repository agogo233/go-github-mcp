package transport

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/agogo233/go-github-mcp/internal/config"
)

const (
	endpointPath = "/mcp"
	maxBodyBytes = 10 << 20 // 10 MiB
)

// RunHTTP serves MCP over Streamable HTTP with optional Bearer shared-secret
// authentication, an unauthenticated /healthz probe, and per-client audit
// logging. WriteTimeout is deliberately zero: the Streamable HTTP transport
// holds long-lived SSE responses that must not be cut by an absolute deadline.
func RunHTTP(ctx context.Context, s *mcpserver.MCPServer, cfg *config.Config) error {
	mcpHTTP := mcpserver.NewStreamableHTTPServer(s, mcpserver.WithEndpointPath(endpointPath))

	mcpMux := http.NewServeMux()
	mcpMux.Handle(endpointPath, mcpHTTP)

	var protected http.Handler = mcpMux
	if cfg.AuthToken != "" {
		protected = bearerAuth(mcpMux, cfg.AuthToken)
	} else if isLoopbackAddr(cfg.HTTPAddr) {
		slog.Warn("HTTP authentication disabled via ALLOW_UNAUTHENTICATED; do not expose beyond loopback")
	} else {
		return fmt.Errorf("refusing to start: ALLOW_UNAUTHENTICATED=true with non-loopback listen address %q "+
			"would expose the GitHub token to anyone who can reach this port", cfg.HTTPAddr)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.Handle(endpointPath, protected)

	handler := limitBody(auditLog(mux), maxBodyBytes)

	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      0, // SSE streams must stay open; IdleTimeout covers reuse
		IdleTimeout:       120 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()

	slog.Info("HTTP server listening", "addr", cfg.HTTPAddr, "endpoint", endpointPath)

	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
}

// bearerAuth enforces "Authorization: Bearer <shared secret>" on every request.
func bearerAuth(next http.Handler, token string) http.Handler {
	// Compare SHA-256 digests instead of raw bytes: ConstantTimeCompare
	// returns early on length mismatch, which would leak the secret length.
	want := sha256.Sum256([]byte(token))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := bearerToken(r)
		gotDigest := sha256.Sum256(got)
		if got == nil || subtle.ConstantTimeCompare(gotDigest[:], want[:]) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="go-github-mcp"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func bearerToken(r *http.Request) []byte {
	h := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if len(h) <= len(prefix) || !strings.EqualFold(h[:len(prefix)], prefix) {
		return nil
	}
	return []byte(strings.TrimSpace(h[len(prefix):]))
}

// limitBody caps request body size for all endpoints.
func limitBody(next http.Handler, n int64) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, n)
		}
		next.ServeHTTP(w, r)
	})
}

// auditLog records who called what. X-Client-Name is self-reported by the
// client and is for operational insight only, not strong attribution.
// The Authorization header is never logged.
func auditLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			// Health probes are high-frequency and carry no audit value.
			next.ServeHTTP(w, r)
			return
		}
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		defer func() {
			slog.Info("http request",
				"client", sanitizeLogField(orUnknown(r.Header.Get("X-Client-Name")), 128),
				"method", r.Method,
				"path", r.URL.Path,
				"status", sw.status,
				"remote", remoteIP(r),
				"duration_ms", time.Since(start).Milliseconds(),
			)
		}()
		next.ServeHTTP(sw, r)
	})
}

// statusWriter captures the response status code for audit logging. It must
// stay transparent to http.Flusher (and other optional interfaces via Unwrap)
// or the Streamable HTTP transport loses its SSE streaming ability.
type statusWriter struct {
	http.ResponseWriter
	status int
}

var _ http.Flusher = (*statusWriter)(nil)

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	return w.ResponseWriter.Write(b)
}

func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap lets http.ResponseController see through this wrapper for deadline
// and flush handling on the underlying connection writer.
func (w *statusWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

// sanitizeLogField strips control characters (notably newlines) so a hostile
// X-Client-Name cannot forge audit log lines, then truncates to n bytes.
func sanitizeLogField(v string, n int) string {
	clean := strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, v)
	if len(clean) <= n {
		return clean
	}
	return clean[:n]
}

func orUnknown(v string) string {
	if strings.TrimSpace(v) == "" {
		return "unknown"
	}
	return v
}

// isLoopbackAddr reports whether the listen address binds only to a loopback
// interface (used to escalate the warning when auth is disabled).
func isLoopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	host = strings.Trim(host, "[]")
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func remoteIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
