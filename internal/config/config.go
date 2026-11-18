// Package config loads and validates all runtime configuration from the environment.
//
// Security notes:
//   - GITHUB_PERSONAL_ACCESS_TOKEN is required and must never be logged.
//   - In HTTP mode AUTH_TOKEN (shared secret) is mandatory unless
//     ALLOW_UNAUTHENTICATED=true is explicitly set (local debugging only).
package config

import (
	"fmt"
	"net"
	"os"
	"strings"
)

const (
	TransportStdio = "stdio"
	TransportHTTP  = "http"
)

// Config holds all runtime settings resolved from environment variables.
type Config struct {
	GitHubToken string
	GitHubHost  string   // optional; empty means github.com
	Toolsets    []string // resolved from GITHUB_TOOLSETS
	Tools       []string // resolved from GITHUB_TOOLS
	ReadOnly    bool

	Transport            string // stdio | http
	HTTPAddr             string
	AuthToken            string // shared secret for HTTP mode
	AllowUnauthenticated bool   // escape hatch for local HTTP debugging
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

	cfg := &Config{
		GitHubToken:          strings.TrimSpace(os.Getenv("GITHUB_PERSONAL_ACCESS_TOKEN")),
		GitHubHost:           strings.TrimSpace(os.Getenv("GITHUB_HOST")),
		ReadOnly:             readOnly,
		Transport:            strings.TrimSpace(os.Getenv("MCP_TRANSPORT")),
		HTTPAddr:             strings.TrimSpace(os.Getenv("HTTP_ADDR")),
		AuthToken:            strings.TrimSpace(os.Getenv("AUTH_TOKEN")),
		AllowUnauthenticated: allowUnauthenticated,
		Toolsets:             parseList(os.Getenv("GITHUB_TOOLSETS")),
		Tools:                parseList(os.Getenv("GITHUB_TOOLS")),
	}

	if cfg.GitHubToken == "" {
		return nil, fmt.Errorf("GITHUB_PERSONAL_ACCESS_TOKEN is required")
	}
	if cfg.GitHubHost != "" && strings.HasPrefix(cfg.GitHubHost, "http://") && !isLoopbackHost(cfg.GitHubHost) {
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
		if cfg.AuthToken == "" && !cfg.AllowUnauthenticated {
			return nil, fmt.Errorf("AUTH_TOKEN is required in HTTP mode (set ALLOW_UNAUTHENTICATED=true only for local debugging)")
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

func isLoopbackHost(host string) bool {
	h := strings.TrimPrefix(host, "http://")
	if i := strings.Index(h, "/"); i >= 0 {
		h = h[:i]
	}
	if _, _, err := net.SplitHostPort(h); err == nil {
		h, _, _ = net.SplitHostPort(h)
	}
	h = strings.Trim(h, "[]")
	return h == "localhost" || h == "127.0.0.1" || h == "::1"
}
