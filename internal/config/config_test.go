package config

import (
	"log/slog"
	"strings"
	"testing"
)

func setEnv(t *testing.T, kv map[string]string) {
	t.Helper()
	for _, k := range []string{
		"GITHUB_PERSONAL_ACCESS_TOKEN", "GITHUB_HOST", "READ_ONLY",
		"MCP_TRANSPORT", "HTTP_ADDR", "GITHUB_MCP_AUTH_TOKEN", "ALLOW_UNAUTHENTICATED",
		"GITHUB_TOOLSETS", "GITHUB_TOOLS",
		"HTTP_RATE_LIMIT", "HTTP_RATE_BURST", "HTTP_MAX_CONCURRENT",
	} {
		t.Setenv(k, "")
	}
	for k, v := range kv {
		t.Setenv(k, v)
	}
}

func TestLoadRequiresToken(t *testing.T) {
	setEnv(t, nil)
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "GITHUB_PERSONAL_ACCESS_TOKEN") {
		t.Fatalf("expected missing-token error, got %v", err)
	}
}

func TestLoadStdioDefaults(t *testing.T) {
	setEnv(t, map[string]string{"GITHUB_PERSONAL_ACCESS_TOKEN": "t"})
	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Transport != TransportStdio {
		t.Errorf("default transport = %q, want stdio", cfg.Transport)
	}
	if len(cfg.Toolsets) != 1 || cfg.Toolsets[0] != "default" {
		t.Errorf("default toolsets = %v, want [default]", cfg.Toolsets)
	}
}

func TestLoadHTTPRequiresAuthTokens(t *testing.T) {
	setEnv(t, map[string]string{
		"GITHUB_PERSONAL_ACCESS_TOKEN": "t",
		"MCP_TRANSPORT":                "http",
	})
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "GITHUB_MCP_AUTH_TOKEN") {
		t.Fatalf("expected GITHUB_MCP_AUTH_TOKEN required error, got %v", err)
	}
	// Explicit escape hatch is allowed.
	setEnv(t, map[string]string{
		"GITHUB_PERSONAL_ACCESS_TOKEN": "t",
		"MCP_TRANSPORT":                "http",
		"ALLOW_UNAUTHENTICATED":        "true",
	})
	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error with escape hatch: %v", err)
	}
	if cfg.HTTPAddr != "0.0.0.0:8080" {
		t.Errorf("default HTTP addr = %q, want 0.0.0.0:8080", cfg.HTTPAddr)
	}
}

func TestLoadHTTPWithAuthTokens(t *testing.T) {
	setEnv(t, map[string]string{
		"GITHUB_PERSONAL_ACCESS_TOKEN": "t",
		"MCP_TRANSPORT":                "http",
		"GITHUB_MCP_AUTH_TOKEN":        "k1:s3cret",
		"HTTP_ADDR":                    "127.0.0.1:9090",
	})
	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.HTTPAddr != "127.0.0.1:9090" {
		t.Errorf("addr = %q, want 127.0.0.1:9090", cfg.HTTPAddr)
	}
	if len(cfg.AuthTokens) != 1 || cfg.AuthTokens[0] != (AuthEntry{Key: "k1", PAT: "s3cret"}) {
		t.Errorf("AuthTokens = %+v, want [{k1 s3cret}]", cfg.AuthTokens)
	}
}

func TestLoadRejectsPlaintextGHESHost(t *testing.T) {
	setEnv(t, map[string]string{
		"GITHUB_PERSONAL_ACCESS_TOKEN": "t",
		"GITHUB_HOST":                  "http://ghe.example.com",
	})
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "https") {
		t.Fatalf("expected plaintext-host error, got %v", err)
	}
	// Loopback http is tolerated for local development.
	setEnv(t, map[string]string{
		"GITHUB_PERSONAL_ACCESS_TOKEN": "t",
		"GITHUB_HOST":                  "http://localhost:3000",
	})
	if _, err := Load(); err != nil {
		t.Fatalf("loopback http host should be allowed, got %v", err)
	}
}

func TestLoadInvalidTransport(t *testing.T) {
	setEnv(t, map[string]string{
		"GITHUB_PERSONAL_ACCESS_TOKEN": "t",
		"MCP_TRANSPORT":                "grpc",
	})
	if _, err := Load(); err == nil {
		t.Fatal("expected invalid transport error, got nil")
	}
}

func TestParseList(t *testing.T) {
	got := parseList(" repos , issues ,, ")
	if len(got) != 2 || got[0] != "repos" || got[1] != "issues" {
		t.Errorf("parseList = %v, want [repos issues]", got)
	}
	if parseList("") != nil {
		t.Error("parseList(\"\") should be nil")
	}
}

func TestParseAuthTokens(t *testing.T) {
	if got, err := parseAuthTokens(""); err != nil || got != nil {
		t.Errorf("parseAuthTokens(\"\") = %v, %v; want nil, nil", got, err)
	}
	got, err := parseAuthTokens("k1:pat1, k2:pat2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []AuthEntry{{Key: "k1", PAT: "pat1"}, {Key: "k2", PAT: "pat2"}}
	if len(got) != len(want) {
		t.Fatalf("parseAuthTokens = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("entry %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestParseAuthTokensRejectsInvalid(t *testing.T) {
	cases := []string{
		"nocolon",            // missing ":"
		":pat",               // empty key
		"key:",               // empty pat
		"key:pat1,key:pat2",  // duplicate key
		"bad key:pat",        // space in key
		"key with colon:pat", // colon in key
		"key:pat,other",      // malformed second entry
	}
	for _, c := range cases {
		if _, err := parseAuthTokens(c); err == nil {
			t.Errorf("parseAuthTokens(%q) expected error, got nil", c)
		}
	}
}

func TestParseAuthTokensRejectsBadKeyCharset(t *testing.T) {
	if _, err := parseAuthTokens("K!9:pat"); err == nil {
		t.Error("parseAuthTokens(\"K!9:pat\") expected charset error, got nil")
	}
	if _, err := parseAuthTokens("abcDEF_-.123:pat"); err != nil {
		t.Errorf("parseAuthTokens(alnum key) unexpected error: %v", err)
	}
}

func TestResolveAuthToken(t *testing.T) {
	cfg := &Config{AuthTokens: []AuthEntry{
		{Key: "personal", PAT: "pat_a"},
		{Key: "bot", PAT: "pat_b"},
	}}
	if pat, ok := cfg.ResolveAuthToken("personal"); !ok || pat != "pat_a" {
		t.Errorf("resolve personal = %q, %v; want pat_a, true", pat, ok)
	}
	if pat, ok := cfg.ResolveAuthToken("bot"); !ok || pat != "pat_b" {
		t.Errorf("resolve bot = %q, %v; want pat_b, true", pat, ok)
	}
	if _, ok := cfg.ResolveAuthToken("unknown"); ok {
		t.Error("resolve unknown should not match")
	}
	if _, ok := cfg.ResolveAuthToken(""); ok {
		t.Error("resolve empty should not match")
	}
}

func TestParseBool(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want bool
		ok   bool
	}{
		{"", false, true},
		{"true", true, true},
		{"TRUE", true, true},
		{"1", true, true},
		{"yes", true, true},
		{"on", true, true},
		{"false", false, true},
		{"0", false, true},
		{"no", false, true},
		{"off", false, true},
		{"ture", false, false}, // typo must fail loudly
		{"maybe", false, false},
	} {
		got, err := parseBool(tc.in)
		if tc.ok && err != nil {
			t.Errorf("parseBool(%q) unexpected error: %v", tc.in, err)
			continue
		}
		if !tc.ok && err == nil {
			t.Errorf("parseBool(%q) expected error, got nil", tc.in)
			continue
		}
		if tc.ok && got != tc.want {
			t.Errorf("parseBool(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestParseLogLevel(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want slog.Level
		ok   bool
	}{
		{"", slog.LevelInfo, true},
		{"info", slog.LevelInfo, true},
		{"INFO", slog.LevelInfo, true},
		{" DEBUG ", slog.LevelDebug, true},
		{"Warn ", slog.LevelWarn, true},
		{"debug", slog.LevelDebug, true},
		{"warn", slog.LevelWarn, true},
		{"warning", slog.LevelWarn, true},
		{"verbose", slog.LevelInfo, false}, // unknown must fail loudly
		{"log", slog.LevelInfo, false},
	} {
		got, err := ParseLogLevel(tc.in)
		if tc.ok && err != nil {
			t.Errorf("ParseLogLevel(%q) unexpected error: %v", tc.in, err)
			continue
		}
		if !tc.ok && err == nil {
			t.Errorf("ParseLogLevel(%q) expected error, got nil", tc.in)
			continue
		}
		if tc.ok && got != tc.want {
			t.Errorf("ParseLogLevel(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestLoadRejectsInvalidReadOnly(t *testing.T) {
	setEnv(t, map[string]string{
		"GITHUB_PERSONAL_ACCESS_TOKEN": "t",
		"READ_ONLY":                    "ture",
	})
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "READ_ONLY") {
		t.Fatalf("expected READ_ONLY invalid boolean error, got %v", err)
	}
}

func TestIsLoopbackHost(t *testing.T) {
	tests := []struct {
		host string
		want bool
	}{
		{"localhost", true},
		{"LOCALHOST", true},
		{"127.0.0.1", true},
		{"127.7.7.7", true},
		{"::1", true},
		{"http://localhost:8080", true},
		{"http://127.0.0.1:9000/api/v3/", true},
		{"example.com", false},
		{"192.168.1.5", false},
		{"::2", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := IsLoopbackHost(tt.host); got != tt.want {
			t.Errorf("IsLoopbackHost(%q) = %v, want %v", tt.host, got, tt.want)
		}
	}
}

func TestParseFloatEnv(t *testing.T) {
	if v, err := parseFloatEnv("", 10); err != nil || v != 10 {
		t.Errorf("parseFloatEnv(\"\") = %v, %v; want 10, nil", v, err)
	}
	if v, err := parseFloatEnv(" 2.5 ", 10); err != nil || v != 2.5 {
		t.Errorf("parseFloatEnv(\" 2.5 \") = %v, %v; want 2.5, nil", v, err)
	}
	// Negative values are accepted here; the semantics layer treats them as
	// "limit disabled".
	if v, err := parseFloatEnv("-1", 10); err != nil || v != -1 {
		t.Errorf("parseFloatEnv(\"-1\") = %v, %v; want -1, nil", v, err)
	}
	for _, bad := range []string{"abc", "NaN", "Inf", "-Inf"} {
		if _, err := parseFloatEnv(bad, 10); err == nil {
			t.Errorf("parseFloatEnv(%q) expected error, got nil", bad)
		}
	}
}

func TestParseIntEnv(t *testing.T) {
	if v, err := parseIntEnv("", 16); err != nil || v != 16 {
		t.Errorf("parseIntEnv(\"\") = %v, %v; want 16, nil", v, err)
	}
	if v, err := parseIntEnv(" 7 ", 16); err != nil || v != 7 {
		t.Errorf("parseIntEnv(\" 7 \") = %v, %v; want 7, nil", v, err)
	}
	if v, err := parseIntEnv("-3", 16); err != nil || v != -3 {
		t.Errorf("parseIntEnv(\"-3\") = %v, %v; want -3, nil", v, err)
	}
	if _, err := parseIntEnv("x", 16); err == nil {
		t.Error("parseIntEnv(\"x\") expected error, got nil")
	}
}
