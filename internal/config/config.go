// Package config loads and validates all runtime configuration from the environment.
//
// Security notes:
//   - GITHUB_PERSONAL_ACCESS_TOKEN is required and must never be logged.
//   - GITHUB_MCP_AUTH_TOKEN maps each MCP Bearer secret to the GitHub PAT it
//     acts as; neither key nor PAT is ever logged.
//   - In HTTP mode GITHUB_MCP_AUTH_TOKEN is mandatory unless
//     ALLOW_UNAUTHENTICATED=true is explicitly set (local debugging only).
package config

import (
	"crypto/sha256"
	"crypto/subtle"
	"fmt"
	"log/slog"
	"math"
	"net"
	"os"
	"strconv"
	"strings"
)

const (
	TransportStdio = "stdio"
	TransportHTTP  = "http"
)

// AuthEntry maps one GITHUB_MCP_AUTH_TOKEN value (the secret an MCP client
// presents as a Bearer token) to the GitHub PAT used for that identity.
type AuthEntry struct {
	Key string // MCP Bearer secret; never logged
	PAT string // GitHub PAT bound to Key; never logged
}

// Config holds all runtime settings resolved from environment variables.
type Config struct {
	GitHubToken string
	GitHubHost  string      // optional; empty means github.com
	AuthTokens  []AuthEntry // resolved from GITHUB_MCP_AUTH_TOKEN
	Toolsets    []string    // resolved from GITHUB_TOOLSETS
	Tools       []string    // resolved from GITHUB_TOOLS
	ReadOnly    bool

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

	authTokens, err := parseAuthTokens(os.Getenv("GITHUB_MCP_AUTH_TOKEN"))
	if err != nil {
		return nil, fmt.Errorf("GITHUB_MCP_AUTH_TOKEN: %w", err)
	}

	cfg := &Config{
		GitHubToken:          strings.TrimSpace(os.Getenv("GITHUB_PERSONAL_ACCESS_TOKEN")),
		GitHubHost:           strings.TrimSpace(os.Getenv("GITHUB_HOST")),
		AuthTokens:           authTokens,
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
		if len(cfg.AuthTokens) == 0 && !cfg.AllowUnauthenticated {
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
