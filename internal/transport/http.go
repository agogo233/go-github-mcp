package transport

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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

	// toolNameProbeLimit caps how much of a tools/call body is buffered just
	// to extract the invoked tool name for audit logs. 64 KiB is enough to
	// reach params.name in practice; larger bodies simply skip the tool-name
	// field instead of materializing up to maxBodyBytes per request.
	toolNameProbeLimit = 64 << 10
)

// RunHTTP serves MCP over Streamable HTTP with optional Bearer shared-secret
// authentication, an unauthenticated /healthz probe, and per-client audit
// logging. WriteTimeout is deliberately zero: the Streamable HTTP transport
// holds long-lived SSE responses that must not be cut by an absolute deadline.
func RunHTTP(ctx context.Context, s *mcpserver.MCPServer, cfg *config.Config) error {
	mcpHTTP := mcpserver.NewStreamableHTTPServer(s, mcpserver.WithEndpointPath(endpointPath))

	mcpMux := http.NewServeMux()
	mcpMux.Handle(endpointPath, mcpHTTP)

	protected := auditLog(mcpMux)
	if cfg.AuthToken != "" {
		// Auth wraps audit so unauthenticated requests are rejected before
		// their body is buffered for tool-name extraction.
		protected = bearerAuth(auditLog(mcpMux), cfg.AuthToken)
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

	// /mcp requests are audited inside the auth wrapper above; healthz needs
	// no audit record.
	handler := limitBody(mux, maxBodyBytes)

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
// client and is for operational insight only, not strong attribution. For
// tools/call requests the invoked tool name is recorded too. The
// Authorization header is never logged.
func auditLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			// Health probes are high-frequency and carry no audit value.
			next.ServeHTTP(w, r)
			return
		}
		tool := mcpToolName(r)
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		defer func() {
			args := []any{
				"client", sanitizeLogField(orUnknown(r.Header.Get("X-Client-Name")), 128),
				"method", r.Method,
				"path", r.URL.Path,
				"status", sw.status,
				"remote", remoteIP(r),
				"duration_ms", time.Since(start).Milliseconds(),
			}
			if tool != "" {
				args = append(args, "tool", tool)
			}
			slog.Info("http request", args...)
		}()
		next.ServeHTTP(sw, r)
	})
}

// probeReader supplies at most max bytes from the underlying body and mirrors
// every byte it hands out into a buffer, so the caller can re-attach the
// probed prefix to the untouched remainder afterwards without losing a byte.
type probeReader struct {
	r    io.Reader
	max  int64
	used int64
	all  bytes.Buffer
}

func (p *probeReader) Read(b []byte) (int, error) {
	if p.used >= p.max {
		return 0, io.EOF
	}
	if rem := p.max - p.used; rem < int64(len(b)) {
		b = b[:rem]
	}
	n, err := p.r.Read(b)
	p.used += int64(n)
	p.all.Write(b[:n])
	return n, err
}

// mcpToolName extracts the invoked tool name from a tools/call JSON-RPC body
// so audit logs show which tool was invoked, not just that /mcp was hit. It
// streams at most the first toolNameProbeLimit bytes and stops as soon as
// params.name has been seen, so requests with large arguments (e.g. embedded
// file contents) are still attributed without buffering their full size. The
// probed bytes are re-attached to the untouched remainder via io.MultiReader,
// so the next handler always receives the complete body. Bodies with a known
// size above the request limit are left untouched so the outer limit
// middleware keeps answering 413.
func mcpToolName(r *http.Request) string {
	if r.Method != http.MethodPost || r.URL.Path != endpointPath || r.Body == nil {
		return ""
	}
	if r.ContentLength > maxBodyBytes {
		return ""
	}
	pr := &probeReader{r: r.Body, max: toolNameProbeLimit}
	name := scanToolName(json.NewDecoder(pr))
	// Re-attach every byte the probe consumed (even on a partial scan) so the
	// transport still sees the complete request body.
	r.Body = io.NopCloser(io.MultiReader(bytes.NewReader(pr.all.Bytes()), r.Body))
	if name == "" {
		return ""
	}
	return sanitizeLogField(name, 128)
}

// scanToolName walks the top level of a JSON-RPC request looking for
// {"method":"tools/call","params":{"name":...}} and returns the tool name.
// Either field order is tolerated, with one caveat: when params comes first
// and its remaining fields (e.g. huge arguments) extend beyond the probe
// window, the scan ends before method can be confirmed and the name is
// skipped. The scan stops as soon as the name is known, the object ends, or
// the request is clearly not a tools/call.
func scanToolName(dec *json.Decoder) string {
	tok, err := dec.Token()
	if err != nil {
		return ""
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return ""
	}
	var method, name string
	for {
		key, ok := nextObjectKey(dec)
		if !ok {
			return ""
		}
		switch key {
		case "method":
			v, err := dec.Token()
			if err != nil {
				return ""
			}
			s, _ := v.(string)
			method = s
			if method != "tools/call" {
				// e.g. initialize: params.name is the server name, not a tool.
				return ""
			}
			if name != "" {
				return name
			}
		case "params":
			// Parse params.name regardless of field order: it only names a
			// tool once the request is known to be a tools/call.
			if !expectObject(dec) {
				return ""
			}
			for {
				pkey, ok := nextObjectKey(dec)
				if !ok {
					break
				}
				if pkey == "name" && name == "" {
					v, err := dec.Token()
					if err != nil {
						return ""
					}
					if s, ok := v.(string); ok {
						name = s
					}
					if method == "tools/call" {
						// Method already confirmed: return immediately so
						// oversized arguments are never pulled through the
						// probe window.
						return name
					}
					continue
				}
				// Skip arguments and any other params fields, nested included.
				if err := skipValue(dec); err != nil {
					return ""
				}
			}
			if method == "tools/call" && name != "" {
				return name
			}
		default:
			if err := skipValue(dec); err != nil {
				return ""
			}
		}
	}
}

func expectObject(dec *json.Decoder) bool {
	tok, err := dec.Token()
	if err != nil {
		return false
	}
	d, ok := tok.(json.Delim)
	return ok && d == '{'
}

// skipValue consumes the next JSON value (object, array or literal) from the
// decoder, including all nested tokens. The standard library has no
// Decoder.Skip, so the token-level skip is implemented here.
func skipValue(dec *json.Decoder) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	d, ok := tok.(json.Delim)
	if !ok || d == '}' || d == ']' {
		return nil // scalar or an already-closed container
	}
	depth := 1
	for depth > 0 {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		switch t := tok.(type) {
		case json.Delim:
			switch t {
			case '{', '[':
				depth++
			case '}', ']':
				depth--
			}
		}
	}
	return nil
}

// nextObjectKey reads the next key of an open object; ok is false once the
// object closes or the input ends mid-object.
func nextObjectKey(dec *json.Decoder) (key string, ok bool) {
	tok, err := dec.Token()
	if err != nil {
		return "", false
	}
	if d, isDelim := tok.(json.Delim); isDelim && d == '}' {
		return "", false
	}
	s, isString := tok.(string)
	return s, isString
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
	return config.IsLoopbackHost(host)
}

func remoteIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
