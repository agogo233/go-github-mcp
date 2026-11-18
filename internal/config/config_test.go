package config

import (
	"strings"
	"testing"
)

func setEnv(t *testing.T, kv map[string]string) {
	t.Helper()
	for _, k := range []string{
		"GITHUB_PERSONAL_ACCESS_TOKEN", "GITHUB_HOST", "READ_ONLY",
		"MCP_TRANSPORT", "HTTP_ADDR", "AUTH_TOKEN", "ALLOW_UNAUTHENTICATED",
		"GITHUB_TOOLSETS", "GITHUB_TOOLS",
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

func TestLoadHTTPRequiresAuthToken(t *testing.T) {
	setEnv(t, map[string]string{
		"GITHUB_PERSONAL_ACCESS_TOKEN": "t",
		"MCP_TRANSPORT":                "http",
	})
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "AUTH_TOKEN") {
		t.Fatalf("expected AUTH_TOKEN required error, got %v", err)
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

func TestLoadHTTPWithAuthToken(t *testing.T) {
	setEnv(t, map[string]string{
		"GITHUB_PERSONAL_ACCESS_TOKEN": "t",
		"MCP_TRANSPORT":                "http",
		"AUTH_TOKEN":                   "s3cret",
		"HTTP_ADDR":                    "127.0.0.1:9090",
	})
	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.HTTPAddr != "127.0.0.1:9090" || cfg.AuthToken != "s3cret" {
		t.Errorf("cfg = %+v, want addr 127.0.0.1:9090 and token s3cret", cfg)
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

func TestLoadRejectsInvalidReadOnly(t *testing.T) {
	setEnv(t, map[string]string{
		"GITHUB_PERSONAL_ACCESS_TOKEN": "t",
		"READ_ONLY":                    "ture",
	})
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "READ_ONLY") {
		t.Fatalf("expected READ_ONLY invalid boolean error, got %v", err)
	}
}
