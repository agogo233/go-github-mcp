package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	mcp "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/agogo233/go-github-mcp/internal/config"
	"github.com/agogo233/go-github-mcp/internal/githubclient"
)

const (
	endpointPath = "/mcp"
	maxBodyBytes = 10 << 20 // 10 MiB

	// toolNameProbeLimit caps how much of a tools/call body is buffered just
	// to extract the invoked tool name and owner argument for audit logs and
	// owner-based routing. 64 KiB is enough to reach params.name and
	// arguments.owner in practice; larger bodies keep whatever was collected
	// within the window instead of materializing up to maxBodyBytes.
	toolNameProbeLimit = 64 << 10
)

// ghLoginKey carries the verified GitHub login of the resolved identity in the
// request context so audit logs can attribute an operation to an account.
// Only the login (a public username) is recorded; the PAT and the MCP Bearer
// secret are never logged.
type ghLoginKey struct{}

// streamableHandler builds the /mcp Streamable HTTP transport. The transport
// is pinned to the latest legacy protocol revision: go-sdk clients on the
// stateless 2026-07-28 core send notifications/cancelled without the
// mandatory _meta protocolVersion stamp, which modern-era validation rejects
// with 400 — permanently poisoning the client connection (every later call
// fails with "client is closing"). Legacy negotiation has no such requirement.
func streamableHandler(s *mcpserver.MCPServer) http.Handler {
	return mcpserver.NewStreamableHTTPServer(s,
		mcpserver.WithEndpointPath(endpointPath),
		mcpserver.WithStreamableHTTPProtocolVersions(mcp.ProtocolVersion20251125),
	)
}

// RunHTTP serves MCP over Streamable HTTP with Bearer authentication, an
// unauthenticated /healthz probe, and per-client audit logging. WriteTimeout
// is deliberately zero: the Streamable HTTP transport holds long-lived SSE
// responses that must not be cut by an absolute deadline.
//
// Two authentication modes exist:
//   - identity (default): logins maps each GITHUB_MCP_AUTH_TOKEN key to the
//     GitHub login it was verified against at startup; the presented key
//     selects the PAT used for the request.
//   - owner (MCP_AUTH_MODE=owner): patLogins maps each routing-table PAT to
//     its verified login; the repository owner argument of the invoked tool
//     selects the PAT, with a random pick from the routing table as fallback.
//
// Both maps feed the gh_login audit field and may be nil when the respective
// mode is unused or authentication is disabled (ALLOW_UNAUTHENTICATED).
func RunHTTP(ctx context.Context, s *mcpserver.MCPServer, cfg *config.Config, logins, patLogins map[string]string) error {
	mcpHTTP := streamableHandler(s)

	mcpMux := http.NewServeMux()
	mcpMux.Handle(endpointPath, mcpHTTP)

	// The default PAT's verified login attributes the ALLOW_UNAUTHENTICATED
	// fallback in audit logs; empty when unknown.
	defaultLogin := patLogins[cfg.GitHubToken]

	// Traffic shaping sits inside the audit wrapper so throttled requests
	// still show up in the audit trail with their 429/503 status. Identity
	// resolution wraps audit so unauthenticated requests are rejected before
	// their body is buffered and the resolved login is available to audit.
	protected := auditLog(rateLimit(mcpMux, cfg))
	if cfg.AuthMode == config.AuthModeOwner {
		// Owner-routing mode: one shared Bearer secret gates the endpoint;
		// the tool call's owner argument selects the GitHub PAT per request.
		protected = ownerRoute(protected, cfg, patLogins)
		if cfg.SharedSecret != "" {
			protected = bearerAuth(protected, cfg)
		} else if isLoopbackAddr(cfg.HTTPAddr) {
			slog.Warn("HTTP authentication disabled via ALLOW_UNAUTHENTICATED; do not expose beyond loopback")
		} else {
			return fmt.Errorf("refusing to start: ALLOW_UNAUTHENTICATED=true with non-loopback listen address %q "+
				"would expose the GitHub token to anyone who can reach this port", cfg.HTTPAddr)
		}
	} else if len(cfg.AuthTokens) > 0 {
		protected = authResolve(protected, cfg, logins, defaultLogin, false)
	} else if isLoopbackAddr(cfg.HTTPAddr) {
		slog.Warn("HTTP authentication disabled via ALLOW_UNAUTHENTICATED; do not expose beyond loopback")
		protected = authResolve(protected, cfg, logins, defaultLogin, true)
	} else {
		return fmt.Errorf("refusing to start: ALLOW_UNAUTHENTICATED=true with non-loopback listen address %q "+
			"would expose the GitHub token to anyone who can reach this port", cfg.HTTPAddr)
	}

	// Repeated authentication failures are throttled per source IP outside
	// the auth middleware, so a blocked source is rejected before it can
	// present another guess.
	protected = authFailGuard(protected)

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

// tokenBucket is a minimal dependency-free rate limiter: tokens refill at
// rate per second up to burst. allow reports whether a token was available
// and, if not, how long until the next one.
type tokenBucket struct {
	mu     sync.Mutex
	rate   float64
	burst  float64
	tokens float64
	last   time.Time
}

func newTokenBucket(rate float64, burst int) *tokenBucket {
	return &tokenBucket{rate: rate, burst: float64(burst), tokens: float64(burst), last: time.Now()}
}

func (b *tokenBucket) allow(now time.Time) (bool, time.Duration) {
	b.mu.Lock()
	defer b.mu.Unlock()
	elapsed := now.Sub(b.last).Seconds()
	b.last = now
	b.tokens = min(b.burst, b.tokens+elapsed*b.rate)
	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	wait := (1 - b.tokens) / b.rate
	// Absurdly small rates would overflow the duration conversion below; an
	// hour of Retry-After is already more than any caller needs to act on.
	const maxWaitSeconds = float64(time.Hour / time.Second)
	if wait > maxWaitSeconds {
		wait = maxWaitSeconds
	}
	return false, time.Duration(wait * float64(time.Second))
}

// rateLimit shapes traffic to the MCP endpoint: a global token bucket bounds
// the sustained request rate and a semaphore bounds in-flight requests. One
// shared budget is deliberate: this is a single-tenant service, and the
// limiter exists so one runaway client cannot exhaust the process or the
// shared GitHub token. The semaphore counts POSTs only: GET /mcp opens
// long-lived SSE notification streams that would otherwise permanently
// occupy slots and starve tool calls. HTTP_RATE_LIMIT <= 0 disables the rate
// bucket, HTTP_MAX_CONCURRENT <= 0 the concurrency cap.
func rateLimit(next http.Handler, cfg *config.Config) http.Handler {
	if cfg.HTTPRateLimit <= 0 && cfg.HTTPMaxConcurrent <= 0 {
		return next
	}
	var bucket *tokenBucket
	if cfg.HTTPRateLimit > 0 {
		bucket = newTokenBucket(cfg.HTTPRateLimit, cfg.HTTPRateBurst)
	}
	var sem chan struct{}
	if cfg.HTTPMaxConcurrent > 0 {
		sem = make(chan struct{}, cfg.HTTPMaxConcurrent)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if sem != nil && r.Method == http.MethodPost {
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			default:
				writeThrottled(w, http.StatusServiceUnavailable, time.Second,
					"server busy: too many concurrent requests")
				return
			}
		}
		if bucket != nil {
			if ok, wait := bucket.allow(time.Now()); !ok {
				writeThrottled(w, http.StatusTooManyRequests, wait,
					"rate limited: slow down and retry")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// writeThrottled rejects a request with a JSON-RPC-shaped error body (same
// shape the MCP transport uses) plus a Retry-After header derived from wait.
func writeThrottled(w http.ResponseWriter, status int, wait time.Duration, message string) {
	seconds := int(math.Ceil(wait.Seconds()))
	if seconds < 1 {
		seconds = 1
	}
	w.Header().Set("Retry-After", strconv.Itoa(seconds))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(mcp.JSONRPCError{
		JSONRPC: mcp.JSONRPC_VERSION,
		Error: mcp.JSONRPCErrorDetails{
			Code:    -32000, // server-defined error
			Message: message,
		},
	})
}

// Repeated authentication failures are throttled per remote IP: after too
// many 401 responses the source is blocked for a short window, so a caller
// cannot probe bearer keys at full speed. The guard sits OUTSIDE the auth
// middleware — which would otherwise reject an unknown key with 401 before
// any block check could run — and counts only 401s; any other outcome clears
// the IP's counter so one noisy user behind a shared NAT gateway is not
// poisoned by another. Fixed constants keep the behavior predictable, and
// the map is capped so a flood of rotating source addresses cannot grow
// memory without bound. Sources behind a shared proxy share one bucket; this
// protection targets direct, single-tenant deployments like the audit field
// it mirrors.
const (
	authFailThreshold   = 20          // 401s before the source is blocked
	authFailBlockWindow = time.Minute // how long a blocked source stays blocked
	authFailMaxEntries  = 4096        // map size bound; reset when exceeded
)

type authFailEntry struct {
	fails        int
	blockedUntil time.Time
}

func authFailGuard(next http.Handler) http.Handler {
	var mu sync.Mutex
	entries := make(map[string]*authFailEntry)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := remoteIP(r)
		now := time.Now()
		mu.Lock()
		if e, ok := entries[ip]; ok && now.Before(e.blockedUntil) {
			wait := e.blockedUntil.Sub(now)
			mu.Unlock()
			writeThrottled(w, http.StatusTooManyRequests, wait,
				"too many failed authentication attempts; retry later")
			return
		}
		mu.Unlock()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)
		mu.Lock()
		defer mu.Unlock()
		if sw.status != http.StatusUnauthorized {
			// Decay instead of clearing: one successful request — possibly
			// made with a different valid key on a multi-identity deployment —
			// must not wash out a sustained guessing campaign from the same
			// source. Halving keeps occasional successes by unrelated users
			// behind a NAT from compounding into a lockout, while a persistent
			// failure stream still crosses the threshold.
			if e := entries[ip]; e != nil {
				e.fails /= 2
				if e.fails == 0 {
					delete(entries, ip)
				}
			}
			return
		}
		if len(entries) >= authFailMaxEntries {
			// Bound memory under a flood of rotating sources, keeping active
			// blocks so the reset cannot unban a source mid-window.
			kept := make(map[string]*authFailEntry, len(entries))
			for k, e := range entries {
				if now.Before(e.blockedUntil) {
					kept[k] = e
				}
			}
			entries = kept
		}
		e := entries[ip]
		if e == nil {
			e = &authFailEntry{}
			entries[ip] = e
		}
		e.fails++
		if e.fails >= authFailThreshold {
			// The counter intentionally survives the block window, so a
			// source that resumes guessing is re-blocked immediately. The
			// window starts now, after the request has been handled, not at
			// the moment the failing request arrived.
			e.blockedUntil = time.Now().Add(authFailBlockWindow)
			if e.fails == authFailThreshold {
				slog.Warn("authentication failures exceeded threshold; blocking source",
					"remote", ip, "window", authFailBlockWindow.String())
			}
		}
	})
}

// authResolve authenticates every request against the configured identities
// and binds the resolved GitHub PAT to the request context so the shared
// GitHub client acts as that identity for the duration of the call.
//
//   - A valid Bearer key resolves to its PAT; the verified login is recorded
//     for the audit trail when known.
//   - allowAnonymous (ALLOW_UNAUTHENTICATED, loopback debugging only) falls
//     back to the default PAT when the key is missing or unknown, preserving
//     the pre-identity behavior of an unauthenticated local deployment.
//     defaultLogin is the verified login of that default PAT so the fallback
//     stays attributable in audit logs.
//   - Otherwise a missing or unknown key is rejected with 401 and a
//     JSON-RPC-shaped error body before the request is buffered or handled.
func authResolve(next http.Handler, cfg *config.Config, logins map[string]string, defaultLogin string, allowAnonymous bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := bearerToken(r)
		if key != nil {
			if pat, ok := cfg.ResolveAuthToken(string(key)); ok {
				ctx := githubclient.WithContextToken(r.Context(), pat)
				if login, ok := logins[string(key)]; ok && login != "" {
					ctx = context.WithValue(ctx, ghLoginKey{}, login)
				}
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}
		}
		if allowAnonymous {
			ctx := githubclient.WithContextToken(r.Context(), cfg.GitHubToken)
			if defaultLogin != "" {
				ctx = context.WithValue(ctx, ghLoginKey{}, defaultLogin)
			}
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}
		w.Header().Set("WWW-Authenticate", `Bearer realm="go-github-mcp"`)
		writeJSONRPCError(w, http.StatusUnauthorized, "unauthorized: unknown or missing MCP auth token")
	})
}

// bearerAuth enforces the single shared MCP Bearer secret of owner-routing
// mode. Every caller presents the same secret; the GitHub identity is chosen
// per request from the tool call's owner argument downstream. Comparison is
// constant time, and failures are rejected with a JSON-RPC-shaped 401 before
// the request body is buffered or handled.
func bearerAuth(next http.Handler, cfg *config.Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if tok := bearerToken(r); tok != nil && cfg.ResolveSharedSecret(string(tok)) {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("WWW-Authenticate", `Bearer realm="go-github-mcp"`)
		writeJSONRPCError(w, http.StatusUnauthorized, "unauthorized: unknown or missing MCP auth token")
	})
}

// routedByKey and ownerKey carry credential-routing attribution for audit
// logs: how the PAT was chosen (routed_by) and which owner argument drove
// the choice. Neither is sensitive; the PAT and the shared secret never are.
type routedByKey struct{}
type ownerKey struct{}

// callInfo is what the body probe extracted from a tools/call request: the
// invoked tool name and, when present, the repository owner argument that
// owner-routing mode routes on. Both fields are best-effort: arguments
// beyond the probe window end the scan and keep what was collected so far.
type callInfo struct {
	tool  string
	owner string
}

// callInfoKey carries the probe result in the request context so the audit
// wrapper does not have to read the body a second time after ownerRoute.
type callInfoKey struct{}

// ownerRoute resolves the GitHub PAT for the request from the repository
// owner argument of the invoked tool (owner-routing mode). The PAT is chosen
// per request: a routing-table hit for the owner, or a random pick from the
// table when the owner is unknown or the tool carries no owner argument, so
// that GitHub rate limits are spread across the configured tokens. The
// verified login of the chosen PAT is bound to the request context for
// audit attribution.
func ownerRoute(next http.Handler, cfg *config.Config, patLogins map[string]string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ci := probeCallInfo(r)
		ctx := context.WithValue(r.Context(), callInfoKey{}, ci)
		pat, routedBy := cfg.ResolveOwnerToken(ci.owner)
		ctx = githubclient.WithContextToken(ctx, pat)
		ctx = context.WithValue(ctx, routedByKey{}, routedBy)
		if ci.owner != "" {
			ctx = context.WithValue(ctx, ownerKey{}, ci.owner)
		}
		if login, ok := patLogins[pat]; ok && login != "" {
			ctx = context.WithValue(ctx, ghLoginKey{}, login)
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// writeJSONRPCError emits a JSON-RPC-shaped error body with the given HTTP
// status, matching the shape the transport uses for throttled requests.
func writeJSONRPCError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(mcp.JSONRPCError{
		JSONRPC: mcp.JSONRPC_VERSION,
		Error: mcp.JSONRPCErrorDetails{
			Code:    -32000, // server-defined error
			Message: message,
		},
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
// tools/call requests the invoked tool name is recorded too, plus the
// credential-routing decision of owner mode (routed_by, owner) when present.
// The Authorization header is never logged.
func auditLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			// Health probes are high-frequency and carry no audit value.
			next.ServeHTTP(w, r)
			return
		}
		var tool string
		if ci, ok := r.Context().Value(callInfoKey{}).(callInfo); ok {
			tool = sanitizeLogField(ci.tool, 128)
		} else {
			tool = mcpToolName(r)
		}
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
			if routedBy, ok := r.Context().Value(routedByKey{}).(string); ok && routedBy != "" {
				args = append(args, "routed_by", routedBy)
			}
			if owner, ok := r.Context().Value(ownerKey{}).(string); ok && owner != "" {
				args = append(args, "owner", sanitizeLogField(owner, 128))
			}
			if login, ok := r.Context().Value(ghLoginKey{}).(string); ok && login != "" {
				args = append(args, "gh_login", login)
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
// so audit logs show which tool was invoked, not just that /mcp was hit.
func mcpToolName(r *http.Request) string {
	ci := probeCallInfo(r)
	if ci.tool == "" {
		return ""
	}
	return sanitizeLogField(ci.tool, 128)
}

// probeCallInfo streams at most the first toolNameProbeLimit bytes of a
// tools/call body to extract the invoked tool name and the repository owner
// argument used by owner-routing mode. It stops as soon as both values are
// known or the probe window ends, so requests with large arguments (e.g.
// embedded file contents) are still attributed without buffering their full
// size. The probed bytes are re-attached to the untouched remainder via
// io.MultiReader, so the next handler always receives the complete body.
// Bodies with a known size above the request limit are left untouched so the
// outer limit middleware keeps answering 413.
func probeCallInfo(r *http.Request) callInfo {
	if r.Method != http.MethodPost || r.URL.Path != endpointPath || r.Body == nil {
		return callInfo{}
	}
	if r.ContentLength > maxBodyBytes {
		return callInfo{}
	}
	pr := &probeReader{r: r.Body, max: toolNameProbeLimit}
	ci := scanCallInfo(json.NewDecoder(pr))
	// Re-attach every byte the probe consumed (even on a partial scan) so the
	// transport still sees the complete request body.
	r.Body = io.NopCloser(io.MultiReader(bytes.NewReader(pr.all.Bytes()), r.Body))
	return ci
}

// scanCallInfo walks the top level of a JSON-RPC request looking for
// {"method":"tools/call","params":{"name":...,"arguments":{...,"owner":...}}}
// and returns the tool name and owner argument. Either field order is
// tolerated, with one caveat: when params comes first and its remaining
// fields (e.g. huge arguments) extend beyond the probe window, the scan ends
// before method can be confirmed and the values are skipped. Conversely, a
// confirmed tools/call keeps everything collected so far when the window
// ends mid-arguments. The scan stops as soon as both values are known, the
// object ends, or the request is clearly not a tools/call.
func scanCallInfo(dec *json.Decoder) callInfo {
	tok, err := dec.Token()
	if err != nil {
		return callInfo{}
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return callInfo{}
	}
	var ci callInfo
	confirmed := false
	for {
		key, ok := nextObjectKey(dec)
		if !ok {
			return partialCallInfo(confirmed, ci)
		}
		switch key {
		case "method":
			v, err := dec.Token()
			if err != nil {
				return callInfo{}
			}
			if s, _ := v.(string); s != "tools/call" {
				// e.g. initialize: params.name is the server name, not a tool.
				return callInfo{}
			}
			confirmed = true
			if ci.tool != "" {
				// Params were parsed before method; the owner came with them.
				return ci
			}
		case "params":
			// Parse params.name and params.arguments.owner regardless of
			// field order; they only name a call once it is a tools/call.
			if !expectObject(dec) {
				return callInfo{}
			}
			for {
				pkey, ok := nextObjectKey(dec)
				if !ok {
					break
				}
				switch pkey {
				case "name":
					if ci.tool == "" {
						v, err := dec.Token()
						if err != nil {
							return partialCallInfo(confirmed, ci)
						}
						if s, ok := v.(string); ok {
							ci.tool = s
						} else if err := skipRest(dec, v); err != nil {
							// Non-string name: consume the whole value so the
							// scan stays aligned with the token stream.
							return partialCallInfo(confirmed, ci)
						}
						continue
					}
					if err := skipValue(dec); err != nil {
						return partialCallInfo(confirmed, ci)
					}
				case "arguments":
					if !expectObject(dec) {
						return partialCallInfo(confirmed, ci)
					}
					for {
						akey, ok := nextObjectKey(dec)
						if !ok {
							break
						}
						if akey == "owner" && ci.owner == "" {
							v, err := dec.Token()
							if err != nil {
								return partialCallInfo(confirmed, ci)
							}
							if s, ok := v.(string); ok {
								ci.owner = s
							} else if err := skipRest(dec, v); err != nil {
								// Non-string owner: consume the whole value to
								// keep the token stream aligned.
								return partialCallInfo(confirmed, ci)
							}
							continue
						}
						// Skip every other argument, nested included, so
						// oversized arguments are never pulled through the
						// probe window one by one.
						if err := skipValue(dec); err != nil {
							return partialCallInfo(confirmed, ci)
						}
					}
				default:
					// Skip arguments and any other params fields.
					if err := skipValue(dec); err != nil {
						return partialCallInfo(confirmed, ci)
					}
				}
			}
			if confirmed {
				return ci
			}
		default:
			if err := skipValue(dec); err != nil {
				return partialCallInfo(confirmed, ci)
			}
		}
	}
}

// partialCallInfo keeps whatever was collected once the request is confirmed
// to be a tools/call: a scan cut short by the probe window must not lose the
// tool name just because huge arguments followed it.
func partialCallInfo(confirmed bool, ci callInfo) callInfo {
	if confirmed {
		return ci
	}
	return callInfo{}
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
	return skipRest(dec, tok)
}

// skipRest consumes the remainder of a JSON value whose first token has
// already been read: scalars are complete after that one token, while
// objects and arrays need their matching close delimiter located. The
// already-closed delimiters '}' and ']' are treated as scalars.
func skipRest(dec *json.Decoder, tok json.Token) error {
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
