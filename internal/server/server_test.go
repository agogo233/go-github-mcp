package server

import (
	"strings"
	"testing"

	"github.com/google/go-github/v72/github"
	mcp "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/agogo233/go-github-mcp/internal/config"
)

func TestToolAllowed(t *testing.T) {
	base := &config.Config{Toolsets: []string{"default"}}
	tests := []struct {
		name     string
		cfg      *config.Config
		toolset  string
		tool     string
		writable bool
		want     bool
	}{
		{"default toolset read tool", base, "issues", "list_issues", false, true},
		{"default toolset write tool", base, "issues", "create_issue", true, true},
		{"non-default toolset disabled", base, "actions", "list_workflows", false, false},
		{"read-only drops write tool", &config.Config{ReadOnly: true, Toolsets: []string{"all"}}, "issues", "create_issue", true, false},
		{"read-only keeps read tool", &config.Config{ReadOnly: true, Toolsets: []string{"all"}}, "issues", "list_issues", false, true},
		{"all enables every toolset", &config.Config{Toolsets: []string{"all"}}, "actions", "list_workflows", true, true},
		{"explicit extra toolset", &config.Config{Toolsets: []string{"default", "actions"}}, "actions", "list_workflows", false, true},
		{"github_tools blocks unlisted tool", &config.Config{Toolsets: []string{"all"}, Tools: []string{"get_me"}}, "issues", "list_issues", false, false},
		{"github_tools allow-list hit", &config.Config{Toolsets: []string{"all"}, Tools: []string{"create_issue"}}, "issues", "create_issue", true, true},
		{"github_tools cannot bypass read-only", &config.Config{ReadOnly: true, Toolsets: []string{"all"}, Tools: []string{"create_issue"}}, "issues", "create_issue", true, false},
		{"github_tools cannot bypass toolsets", &config.Config{Toolsets: []string{"default"}, Tools: []string{"list_workflows"}}, "actions", "list_workflows", false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, reason := toolAllowed(tt.cfg, tt.toolset, tt.tool, tt.writable)
			if got != tt.want {
				t.Fatalf("toolAllowed(%q, %q, writable=%v) = %v (reason %q), want %v",
					tt.toolset, tt.tool, tt.writable, got, reason, tt.want)
			}
			if !got && reason == "" {
				t.Error("rejected tool must carry a reason")
			}
		})
	}
}

func TestValidToolsetsHelpMatchesKnownToolsets(t *testing.T) {
	listed := map[string]bool{}
	for _, name := range strings.Split(validToolsetsHelp, ", ") {
		listed[name] = true
	}
	for name := range knownToolsets {
		if !listed[name] {
			t.Errorf("toolset %q is in knownToolsets but missing from validToolsetsHelp", name)
		}
	}
	for name := range listed {
		if !knownToolsets[name] {
			t.Errorf("validToolsetsHelp lists %q which is not in knownToolsets", name)
		}
	}
}

func TestNewRejectsUnknownToolset(t *testing.T) {
	cfg := &config.Config{Toolsets: []string{"default", "pullrequests"}}
	s, err := New(cfg, github.NewClient(nil), "test")
	if err == nil {
		t.Fatal("expected error for unknown toolset, got nil")
	}
	if s != nil {
		t.Error("server must be nil when configuration is rejected")
	}
	if !strings.Contains(err.Error(), "pullrequests") {
		t.Errorf("error should name the offending toolset, got: %v", err)
	}
}

func TestNewAcceptsValidToolsets(t *testing.T) {
	cfg := &config.Config{Toolsets: []string{"default", "git", "labels", "actions"}}
	s, err := New(cfg, github.NewClient(nil), "test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s == nil {
		t.Fatal("expected non-nil server")
	}
}

// countingRegistrar implements tools.Registrar and counts registered tools.
type countingRegistrar struct{ n int }

func (c *countingRegistrar) Add(string, bool, mcp.Tool, mcpserver.ToolHandlerFunc) { c.n++ }

func TestToolsetRegistryIsComplete(t *testing.T) {
	if len(toolsetRegistry) != 18 {
		t.Fatalf("toolsetRegistry has %d entries, want 18", len(toolsetRegistry))
	}
	seen := map[string]bool{}
	for _, e := range toolsetRegistry {
		if e.name == "" || e.register == nil {
			t.Errorf("registry entry %q must have a name and a register func", e.name)
		}
		if seen[e.name] {
			t.Errorf("duplicate toolset %q in registry", e.name)
		}
		seen[e.name] = true
	}
}

// The registry is the single source of truth; this pins the documented
// contract of 18 toolsets registering 77 tools in total.
func TestRegistryRegistersAllTools(t *testing.T) {
	c := &countingRegistrar{}
	for _, e := range toolsetRegistry {
		e.register(c, github.NewClient(nil))
	}
	if c.n != 77 {
		t.Fatalf("registry registered %d tools, want 77", c.n)
	}
}
