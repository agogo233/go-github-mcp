// Package config loads and validates all runtime configuration from the environment.
//
// Security notes:
//   - GITHUB_PERSONAL_ACCESS_TOKEN is required and must never be logged.
//   - In the default identity mode, GITHUB_MCP_AUTH_TOKEN maps each MCP
//     Bearer secret to the GitHub PAT it acts as; neither key nor PAT is ever
//     logged. In owner mode (MCP_AUTH_MODE=owner) the same variable holds one
//     shared Bearer secret, and GITHUB_OWNER_TOKENS maps repository owners to
//     PATs; neither secret nor PAT is ever logged.
//   - In HTTP mode the respective auth variable is mandatory unless
//     ALLOW_UNAUTHENTICATED=true is explicitly set (local debugging only).
package config

import (
	"crypto/sha256"
	"crypto/subtle"
	"fmt"
	"log/slog"
	"math"
	"math/rand/v2"
	"net"
	"os"
	"strconv"
	"strings"
)

const (
	TransportStdio = "stdio"
	TransportHTTP  = "http"

	// AuthModeIdentity is the default: each MCP Bearer key maps to one PAT.
	AuthModeIdentity = "identity"
	// AuthModeOwner routes by the repository owner argument of the invoked
	// tool; all callers share one Bearer secret.
	AuthModeOwner = "owner"
)

// How a request's PAT was chosen; recorded in audit logs as routed_by.
const (
	RouteOwner   = "owner"   // routing-table hit for the repository owner
	RouteRandom  = "random"  // random pick from the routing table (fallback)
	RouteDefault = "default" // default PAT (empty routing table, debug only)
)

// AuthEntry is one credential binding. In identity mode Key is the MCP
// Bearer secret a client presents; in owner mode Key is a GitHub login
// (user or org) matched against the tool call's owner argument. PAT is the
// GitHub identity used for it. Neither value is ever logged.
type AuthEntry struct {
	Key string // MCP Bearer secret or GitHub owner; never logged
	PAT string // GitHub PAT bound to Key; never logged
}

// Config holds all runtime settings resolved from environment variables.
type Config struct {
	GitHubToken string
	GitHubHost  string      // optional; empty means github.com
	AuthTokens  []AuthEntry // identity mode: resolved from GITHUB_MCP_AUTH_TOKEN
	Toolsets    []string    // resolved from GITHUB_TOOLSETS
	Tools       []string    // resolved from GITHUB_TOOLS
	ReadOnly    bool

	AuthMode     string      // AuthModeIdentity (default) or AuthModeOwner
	SharedSecret string      // owner mode: the single shared MCP Bearer secret; never logged
	OwnerTokens  []AuthEntry // owner mode: owner → PAT routing table from GITHUB_OWNER_TOKENS

	Transport            string // stdio | http
	HTTPAddr             string
	AllowUnauthenticated bool // escape hatch for local HTTP debugging

	// HTTP traffic-shaping limits (HTTP mode only). RateLimit <= 0 or
	// MaxConcurrent <= 0 disables the respective limit.
	HTTPRateLimit     float64 // sustained requests per second to /mcp
	HTTPRateBurst     int     // burst size on top of the sustained rate
	HTTPMaxConcurrent int     // in-flight /mcp requests allowed at once
}

// Load reads the environment and returns a validated Config.
func Load() (*Config, error) {
	readOnly, err := parseBool(os.Getenv("READ_ONLY"))
	if err != nil {
		return nil, fmt.Errorf("READ_ONLY: %w", err)
	}
	allowUnauthenticated, err := parseBool(os.Getenv("ALLOW_UNAUTHENTICATED"))
	if err != nil {
		return nil, fmt.Errorf("ALLOW_UNAUTHENTICATED: %w", err)
	}
	rateLimit, err := parseFloatEnv(os.Getenv("HTTP_RATE_LIMIT"), 10)
	if err != nil {
		return nil, fmt.Errorf("HTTP_RATE_LIMIT: %w", err)
	}
	rateBurst, err := parseIntEnv(os.Getenv("HTTP_RATE_BURST"), 20)
	if err != nil {
		return nil, fmt.Errorf("HTTP_RATE_BURST: %w", err)
	}
	maxConcurrent, err := parseIntEnv(os.Getenv("HTTP_MAX_CONCURRENT"), 16)
	if err != nil {
		return nil, fmt.Errorf("HTTP_MAX_CONCURRENT: %w", err)
	}
	if rateLimit > 0 && rateBurst < 1 {
		rateBurst = 1
	}

	authMode := strings.TrimSpace(os.Getenv("MCP_AUTH_MODE"))
	switch authMode {
	case "", AuthModeIdentity:
		authMode = AuthModeIdentity
	case AuthModeOwner:
	default:
		return nil, fmt.Errorf("MCP_AUTH_MODE must be %q or %q, got %q", AuthModeIdentity, AuthModeOwner, authMode)
	}

	var (
		authTokens   []AuthEntry
		sharedSecret string
		ownerTokens  []AuthEntry
	)
	rawAuth := os.Getenv("GITHUB_MCP_AUTH_TOKEN")
	if authMode == AuthModeOwner {
		// Owner-routing mode: GITHUB_MCP_AUTH_TOKEN is one shared Bearer
		// secret for every caller, and GITHUB_OWNER_TOKENS maps repository
		// owners to the PAT used when a tool call targets them.
		sharedSecret = strings.TrimSpace(rawAuth)
		ownerTokens, err = parseOwnerTokens(os.Getenv("GITHUB_OWNER_TOKENS"))
		if err != nil {
			return nil, fmt.Errorf("GITHUB_OWNER_TOKENS: %w", err)
		}
	} else {
		authTokens, err = parseAuthTokens(rawAuth)
		if err != nil {
			return nil, fmt.Errorf("GITHUB_MCP_AUTH_TOKEN: %w", err)
		}
	}

	cfg := &Config{
		GitHubToken:          strings.TrimSpace(os.Getenv("GITHUB_PERSONAL_ACCESS_TOKEN")),
		GitHubHost:           strings.TrimSpace(os.Getenv("GITHUB_HOST")),
		AuthMode:             authMode,
		AuthTokens:           authTokens,
		SharedSecret:         sharedSecret,
		OwnerTokens:          ownerTokens,
		ReadOnly:             readOnly,
		Transport:            strings.TrimSpace(os.Getenv("MCP_TRANSPORT")),
		HTTPAddr:             strings.TrimSpace(os.Getenv("HTTP_ADDR")),
		AllowUnauthenticated: allowUnauthenticated,
		Toolsets:             parseList(os.Getenv("GITHUB_TOOLSETS")),
		Tools:                parseList(os.Getenv("GITHUB_TOOLS")),
		HTTPRateLimit:        rateLimit,
		HTTPRateBurst:        rateBurst,
		HTTPMaxConcurrent:    maxConcurrent,
	}

	if cfg.GitHubToken == "" {
		return nil, fmt.Errorf("GITHUB_PERSONAL_ACCESS_TOKEN is required")
	}
	if cfg.GitHubHost != "" && strings.HasPrefix(cfg.GitHubHost, "http://") && !IsLoopbackHost(cfg.GitHubHost) {
		return nil, fmt.Errorf("GITHUB_HOST must use https (got %q); plaintext would leak the token", cfg.GitHubHost)
	}

	switch cfg.Transport {
	case "", TransportStdio:
		cfg.Transport = TransportStdio
	case TransportHTTP:
	default:
		return nil, fmt.Errorf("MCP_TRANSPORT must be %q or %q, got %q", TransportStdio, TransportHTTP, cfg.Transport)
	}

	if cfg.Transport == TransportHTTP {
		if cfg.HTTPAddr == "" {
			cfg.HTTPAddr = "0.0.0.0:8080"
		}
		switch {
		case cfg.AuthMode == AuthModeOwner:
			if cfg.SharedSecret == "" && !cfg.AllowUnauthenticated {
				return nil, fmt.Errorf("GITHUB_MCP_AUTH_TOKEN is required in HTTP owner mode (set ALLOW_UNAUTHENTICATED=true only for local debugging)")
			}
			if len(cfg.OwnerTokens) == 0 && !cfg.AllowUnauthenticated {
				return nil, fmt.Errorf("GITHUB_OWNER_TOKENS is required in HTTP owner mode (set ALLOW_UNAUTHENTICATED=true only for local debugging)")
			}
		case len(cfg.AuthTokens) == 0 && !cfg.AllowUnauthenticated:
			return nil, fmt.Errorf("GITHUB_MCP_AUTH_TOKEN is required in HTTP mode (set ALLOW_UNAUTHENTICATED=true only for local debugging)")
		}
	}

	if len(cfg.Toolsets) == 0 {
		cfg.Toolsets = []string{"default"}
	}
	return cfg, nil
}

// GitHubHostOrDefault returns the configured host or "github.com".
func (c *Config) GitHubHostOrDefault() string {
	if c.GitHubHost == "" {
		return "github.com"
	}
	return c.GitHubHost
}

// ResolveAuthToken returns the GitHub PAT bound to the MCP Bearer secret key.
// Comparison uses SHA-256 digests with subtle.ConstantTimeCompare, mirroring
// the auth middleware's constant-time check so a timing side channel cannot
// reveal which key (if any) matches. It returns ok=false when key is not one
// of the configured identities.
func (c *Config) ResolveAuthToken(key string) (string, bool) {
	got := sha256.Sum256([]byte(key))
	for _, e := range c.AuthTokens {
		want := sha256.Sum256([]byte(e.Key))
		if subtle.ConstantTimeCompare(got[:], want[:]) == 1 {
			return e.PAT, true
		}
	}
	return "", false
}

// ResolveSharedSecret reports whether presented matches the single shared
// MCP Bearer secret of owner-routing mode. Comparison uses SHA-256 digests
// with subtle.ConstantTimeCompare, mirroring the identity resolver, so a
// timing side channel cannot reveal the secret. An unset secret never
// matches, defense in depth for callers that skip the config validation.
func (c *Config) ResolveSharedSecret(presented string) bool {
	if c.SharedSecret == "" {
		return false
	}
	got := sha256.Sum256([]byte(presented))
	want := sha256.Sum256([]byte(c.SharedSecret))
	return subtle.ConstantTimeCompare(got[:], want[:]) == 1
}

// ResolveOwnerToken maps a repository owner (a GitHub user or org login) to
// the GitHub PAT used for calls targeting it. Matching is case-insensitive
// because GitHub logins are. When the owner has several PATs configured, one
// is chosen at random to spread GitHub rate limits across tokens; owners
// without a routing entry — and tools without an owner argument — fall back
// to a random pick from the whole table. routedBy reports how the PAT was
// chosen: RouteOwner, RouteRandom, or RouteDefault when the table is empty.
func (c *Config) ResolveOwnerToken(owner string) (pat string, routedBy string) {
	if len(c.OwnerTokens) == 0 {
		return c.GitHubToken, RouteDefault
	}
	var matches []string
	for _, e := range c.OwnerTokens {
		if strings.EqualFold(e.Key, owner) {
			matches = append(matches, e.PAT)
		}
	}
	if len(matches) > 0 {
		return matches[rand.IntN(len(matches))], RouteOwner
	}
	for _, e := range c.OwnerTokens {
		matches = append(matches, e.PAT)
	}
	return matches[rand.IntN(len(matches))], RouteRandom
}

// parseAuthTokens parses a comma-separated list of "key:pat" pairs mapping
// each MCP Bearer secret to the GitHub PAT it acts as. Keys must be unique
// and match a restricted character set; PATs must be non-empty. A pair is
// split at the first colon so a PAT containing colons stays valid.
func parseAuthTokens(v string) ([]AuthEntry, error) {
	if strings.TrimSpace(v) == "" {
		return nil, nil
	}
	seen := make(map[string]bool)
	var out []AuthEntry
	for i, part := range strings.Split(v, ",") {
		key, pat, ok := strings.Cut(strings.TrimSpace(part), ":")
		key = strings.TrimSpace(key)
		pat = strings.TrimSpace(pat)
		// Error messages name the position and shape only; neither the key
		// nor the PAT value is echoed back, so a malformed variable can never
		// leak a secret into the startup log.
		if !ok || key == "" || pat == "" {
			return nil, fmt.Errorf("entry %d is malformed (want key:pat)", i+1)
		}
		if !validAuthKey(key) {
			return nil, fmt.Errorf("entry %d has an invalid key (want [A-Za-z0-9_.-]{1,128})", i+1)
		}
		if seen[key] {
			return nil, fmt.Errorf("entry %d repeats a key already used by an earlier entry", i+1)
		}
		seen[key] = true
		out = append(out, AuthEntry{Key: key, PAT: pat})
	}
	return out, nil
}

// parseOwnerTokens parses a comma-separated list of "owner:pat" pairs mapping
// a GitHub login (user or org) to the PAT used when a tool call targets that
// owner. Unlike MCP auth keys, the same owner may appear several times with
// different PATs so that GitHub rate limits are spread across tokens; only
// an exact repeat of an owner:pat pair is rejected. Error messages name the
// position and shape only; neither the owner nor the PAT value is echoed
// back, so a malformed variable can never leak a secret into the log.
func parseOwnerTokens(v string) ([]AuthEntry, error) {
	if strings.TrimSpace(v) == "" {
		return nil, nil
	}
	seen := make(map[string]bool)
	var out []AuthEntry
	for i, part := range strings.Split(v, ",") {
		owner, pat, ok := strings.Cut(strings.TrimSpace(part), ":")
		owner = strings.TrimSpace(owner)
		pat = strings.TrimSpace(pat)
		if !ok || owner == "" || pat == "" {
			return nil, fmt.Errorf("entry %d is malformed (want owner:pat)", i+1)
		}
		if !validAuthKey(owner) {
			return nil, fmt.Errorf("entry %d has an invalid owner (want [A-Za-z0-9_.-]{1,128})", i+1)
		}
		pair := strings.ToLower(owner) + "\x00" + pat
		if seen[pair] {
			return nil, fmt.Errorf("entry %d repeats an earlier owner:pat pair", i+1)
		}
		seen[pair] = true
		out = append(out, AuthEntry{Key: owner, PAT: pat})
	}
	return out, nil
}

// validAuthKey reports whether k is an acceptable MCP Bearer secret. The
// restricted character set keeps keys safe for env files and logs (only the
// key's existence matters to a resolver, never its value).
func validAuthKey(k string) bool {
	if len(k) == 0 || len(k) > 128 {
		return false
	}
	for _, r := range k {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '_' || r == '.' || r == '-':
		default:
			return false
		}
	}
	return true
}

// parseList splits a comma-separated env value into trimmed, non-empty items.
func parseList(v string) []string {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	var out []string
	for _, item := range strings.Split(v, ",") {
		if s := strings.TrimSpace(item); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// parseFloatEnv parses a float environment value; empty means def. Used for
// traffic-shaping knobs where zero or a negative value disables the limit.
func parseFloatEnv(v string, def float64) (float64, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return def, nil
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid number %q (use e.g. 10 or 2.5)", v)
	}
	// NaN makes every comparison false (permanent 429) and Inf disables the
	// limiter silently; both spellings parse as floats, so reject them.
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, fmt.Errorf("invalid number %q (must be finite)", v)
	}
	return f, nil
}

// parseIntEnv parses an int environment value; empty means def.
func parseIntEnv(v string, def int) (int, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("invalid integer %q", v)
	}
	return n, nil
}

// ParseLogLevel maps the LOG_LEVEL environment value to a slog level.
// Empty means info. Unknown values fail loudly so a typo does not silently
// change log verbosity.
func ParseLogLevel(v string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "":
		return slog.LevelInfo, nil
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	default:
		return slog.LevelInfo, fmt.Errorf("invalid log level %q (use debug/info/warn/warning)", v)
	}
}

// parseBool accepts common truthy/falsy spellings and rejects anything else
// so that a typo in a safety switch fails loudly instead of defaulting.
func parseBool(v string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "":
		return false, nil
	case "1", "true", "yes", "y", "on":
		return true, nil
	case "0", "false", "no", "n", "off":
		return false, nil
	default:
		return false, fmt.Errorf("invalid boolean value %q (use true/false/1/0/yes/no/on/off)", v)
	}
}

// IsLoopbackHost reports whether host (a bare host, host:port or URL) points
// at a loopback interface. It relaxes the https requirement for local
// GitHub Enterprise setups and drives the auth-disabled warning for
// loopback-only listen addresses.
func IsLoopbackHost(host string) bool {
	h := host
	if i := strings.Index(h, "://"); i >= 0 {
		h = h[i+3:]
	}
	if i := strings.IndexAny(h, "/?#"); i >= 0 {
		h = h[:i]
	}
	if _, _, err := net.SplitHostPort(h); err == nil {
		h, _, _ = net.SplitHostPort(h)
	}
	h = strings.Trim(h, "[]")
	if strings.EqualFold(h, "localhost") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}
