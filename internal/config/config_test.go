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
		"MCP_AUTH_MODE", "GITHUB_OWNER_TOKENS",
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

func TestLoadAuthModeDefaultsToIdentity(t *testing.T) {
	setEnv(t, map[string]string{
		"GITHUB_PERSONAL_ACCESS_TOKEN": "t",
		"MCP_TRANSPORT":                "http",
		"GITHUB_MCP_AUTH_TOKEN":        "k1:pat1",
	})
	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.AuthMode != AuthModeIdentity {
		t.Errorf("default auth mode = %q, want %q", cfg.AuthMode, AuthModeIdentity)
	}
	if len(cfg.AuthTokens) != 1 || cfg.OwnerTokens != nil || cfg.SharedSecret != "" {
		t.Errorf("identity mode must parse key:pat pairs and leave owner fields empty, got %+v", cfg)
	}
}

func TestLoadRejectsInvalidAuthMode(t *testing.T) {
	setEnv(t, map[string]string{
		"GITHUB_PERSONAL_ACCESS_TOKEN": "t",
		"MCP_AUTH_MODE":                "byowner",
	})
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "MCP_AUTH_MODE") {
		t.Fatalf("expected invalid MCP_AUTH_MODE error, got %v", err)
	}
}

func TestLoadOwnerMode(t *testing.T) {
	setEnv(t, map[string]string{
		"GITHUB_PERSONAL_ACCESS_TOKEN": "t",
		"MCP_TRANSPORT":                "http",
		"MCP_AUTH_MODE":                "owner",
		"GITHUB_MCP_AUTH_TOKEN":        "shared-secret",
		"GITHUB_OWNER_TOKENS":          "alice:pat_a,alice:pat_a2,MyOrg:pat_org",
	})
	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.AuthMode != AuthModeOwner {
		t.Errorf("auth mode = %q, want %q", cfg.AuthMode, AuthModeOwner)
	}
	if cfg.SharedSecret != "shared-secret" {
		t.Errorf("SharedSecret = %q, want shared-secret", cfg.SharedSecret)
	}
	want := []AuthEntry{{Key: "alice", PAT: "pat_a"}, {Key: "alice", PAT: "pat_a2"}, {Key: "MyOrg", PAT: "pat_org"}}
	if len(cfg.OwnerTokens) != len(want) {
		t.Fatalf("OwnerTokens = %+v, want %+v", cfg.OwnerTokens, want)
	}
	for i := range want {
		if cfg.OwnerTokens[i] != want[i] {
			t.Errorf("OwnerTokens[%d] = %+v, want %+v", i, cfg.OwnerTokens[i], want[i])
		}
	}
}

func TestLoadOwnerModeRequiresSecretAndOwners(t *testing.T) {
	base := map[string]string{
		"GITHUB_PERSONAL_ACCESS_TOKEN": "t",
		"MCP_TRANSPORT":                "http",
		"MCP_AUTH_MODE":                "owner",
	}
	setEnv(t, base)
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "GITHUB_MCP_AUTH_TOKEN") {
		t.Fatalf("expected missing shared secret error, got %v", err)
	}
	setEnv(t, map[string]string{
		"GITHUB_PERSONAL_ACCESS_TOKEN": "t",
		"MCP_TRANSPORT":                "http",
		"MCP_AUTH_MODE":                "owner",
		"GITHUB_MCP_AUTH_TOKEN":        "shared-secret",
	})
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "GITHUB_OWNER_TOKENS") {
		t.Fatalf("expected missing GITHUB_OWNER_TOKENS error, got %v", err)
	}
	// The escape hatch keeps local debugging possible without either variable.
	setEnv(t, map[string]string{
		"GITHUB_PERSONAL_ACCESS_TOKEN": "t",
		"MCP_TRANSPORT":                "http",
		"MCP_AUTH_MODE":                "owner",
		"ALLOW_UNAUTHENTICATED":        "true",
	})
	if _, err := Load(); err != nil {
		t.Fatalf("unexpected error with escape hatch: %v", err)
	}
}

func TestParseOwnerTokens(t *testing.T) {
	if got, err := parseOwnerTokens(""); err != nil || got != nil {
		t.Errorf("parseOwnerTokens(\"\") = %v, %v; want nil, nil", got, err)
	}
	got, err := parseOwnerTokens("alice:pat_a, bob:pat_b ")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []AuthEntry{{Key: "alice", PAT: "pat_a"}, {Key: "bob", PAT: "pat_b"}}
	if len(got) != len(want) {
		t.Fatalf("parseOwnerTokens = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("entry %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	// The same owner may carry several PATs to spread rate limits.
	if _, err := parseOwnerTokens("alice:pat_a,alice:pat_a2"); err != nil {
		t.Errorf("repeated owner with different PATs should be allowed, got %v", err)
	}
}

func TestParseOwnerTokensRejectsInvalid(t *testing.T) {
	cases := []string{
		"nocolon",                 // missing ":"
		":pat",                    // empty owner
		"alice:",                  // empty pat
		"alice:pat_a,alice:pat_a", // exact duplicate pair
		"bad owner:pat",           // space in owner
		"alice:pat,other",         // malformed second entry
	}
	for _, c := range cases {
		if _, err := parseOwnerTokens(c); err == nil {
			t.Errorf("parseOwnerTokens(%q) expected error, got nil", c)
		}
	}
}

func TestResolveOwnerToken(t *testing.T) {
	cfg := &Config{
		GitHubToken: "default-pat",
		OwnerTokens: []AuthEntry{
			{Key: "alice", PAT: "pat_a1"},
			{Key: "alice", PAT: "pat_a2"},
			{Key: "myorg", PAT: "pat_org"},
		},
	}
	// A single-entry owner always resolves to its PAT.
	single := &Config{OwnerTokens: []AuthEntry{{Key: "alice", PAT: "pat_a1"}}}
	if pat, routed := single.ResolveOwnerToken("alice"); pat != "pat_a1" || routed != RouteOwner {
		t.Errorf("single entry = %q, %q; want pat_a1, owner", pat, routed)
	}
	// Matching is case-insensitive, mirroring GitHub logins.
	if pat, routed := single.ResolveOwnerToken("ALICE"); pat != "pat_a1" || routed != RouteOwner {
		t.Errorf("case-insensitive hit = %q, %q; want pat_a1, owner", pat, routed)
	}
	// Several PATs for one owner: the pick stays within that owner's PATs.
	seen := map[string]bool{}
	for i := 0; i < 64; i++ {
		pat, routed := cfg.ResolveOwnerToken("Alice")
		if routed != RouteOwner {
			t.Fatalf("owner hit routed_by = %q, want %q", routed, RouteOwner)
		}
		if pat != "pat_a1" && pat != "pat_a2" {
			t.Fatalf("owner hit pat = %q, want one of alice's PATs", pat)
		}
		seen[pat] = true
	}
	if len(seen) != 2 {
		t.Errorf("expected both alice PATs to be used across picks, got %v", seen)
	}
	// Unknown owners fall back to a random pick from the whole table.
	pool := map[string]bool{"pat_a1": true, "pat_a2": true, "pat_org": true}
	for i := 0; i < 128; i++ {
		pat, routed := cfg.ResolveOwnerToken("nobody")
		if routed != RouteRandom {
			t.Fatalf("fallback routed_by = %q, want %q", routed, RouteRandom)
		}
		if !pool[pat] {
			t.Fatalf("fallback pat = %q, want one of the routing table", pat)
		}
	}
	// Tools without an owner argument behave like unknown owners.
	if _, routed := cfg.ResolveOwnerToken(""); routed != RouteRandom {
		t.Errorf("empty owner routed_by = %q, want %q", routed, RouteRandom)
	}
	// An empty routing table (debug deployments) uses the default PAT.
	empty := &Config{GitHubToken: "default-pat"}
	if pat, routed := empty.ResolveOwnerToken("alice"); pat != "default-pat" || routed != RouteDefault {
		t.Errorf("empty table = %q, %q; want default-pat, default", pat, routed)
	}
}

func TestResolveSharedSecret(t *testing.T) {
	cfg := &Config{SharedSecret: "shared-secret"}
	if !cfg.ResolveSharedSecret("shared-secret") {
		t.Error("ResolveSharedSecret should accept the configured secret")
	}
	if cfg.ResolveSharedSecret("shared-secreT") {
		t.Error("ResolveSharedSecret must reject a different secret")
	}
	if cfg.ResolveSharedSecret("") {
		t.Error("ResolveSharedSecret must reject an empty secret")
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
