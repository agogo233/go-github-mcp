// Package server assembles the MCP server, applies toolset filtering and
// read-only policy, and registers all tool groups.
package server

import (
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"github.com/google/go-github/v72/github"
	mcp "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/agogo233/go-github-mcp/internal/config"
	"github.com/agogo233/go-github-mcp/internal/tools"
)

const serverName = "go-github-mcp"

// toolsetEntry is one selectable toolset and its registration hook.
type toolsetEntry struct {
	name     string
	register func(tools.Registrar, *github.Client)
}

// toolsetRegistry lists every toolset the server can register. Adding a
// toolset means adding one entry here; the known-name set and the help string
// are derived from this table, so there is a single source of truth.
var toolsetRegistry = []toolsetEntry{
	{"context", tools.RegisterContext},
	{"users", tools.RegisterUsers},
	{"repos", tools.RegisterRepos},
	{"git", tools.RegisterGit},
	{"issues", tools.RegisterIssues},
	{"labels", tools.RegisterLabels},
	{"pull_requests", tools.RegisterPullRequests},
	{"actions", tools.RegisterActions},
	{"notifications", tools.RegisterNotifications},
	{"gists", tools.RegisterGists},
	{"discussions", tools.RegisterDiscussions},
	{"stargazers", tools.RegisterStargazers},
	{"orgs", tools.RegisterOrgs},
	{"code_security", tools.RegisterCodeSecurity},
	{"secret_protection", tools.RegisterSecretProtection},
	{"dependabot", tools.RegisterDependabot},
	{"security_advisories", tools.RegisterSecurityAdvisories},
	{"projects", tools.RegisterProjects},
}

// defaultToolsets mirrors the official github-mcp-server default toolset set.
var defaultToolsets = map[string]bool{
	"context":       true,
	"repos":         true,
	"issues":        true,
	"pull_requests": true,
	"users":         true,
}

// knownToolsets lists every toolset name the server can register, plus the
// special selectors "all" and "default".
var knownToolsets = func() map[string]bool {
	m := map[string]bool{"all": true, "default": true}
	for _, e := range toolsetRegistry {
		m[e.name] = true
	}
	return m
}()

// validToolsetsHelp lists every selectable toolset for error messages.
var validToolsetsHelp = func() string {
	names := make([]string, 0, len(toolsetRegistry)+2)
	names = append(names, "all", "default")
	for _, e := range toolsetRegistry {
		names = append(names, e.name)
	}
	return strings.Join(names, ", ")
}()

// Registrar wraps the MCP server and applies configuration policy
// (toolset allow-list and read-only mode) before tools are registered.
type Registrar struct {
	s   *mcpserver.MCPServer
	cfg *config.Config
}

// New builds the MCP server with every configured tool registered. It fails
// fast when GITHUB_TOOLSETS contains an unknown toolset name.
func New(cfg *config.Config, client *github.Client, version string) (*mcpserver.MCPServer, error) {
	for _, t := range cfg.Toolsets {
		if !knownToolsets[t] {
			return nil, fmt.Errorf("unknown toolset %q in GITHUB_TOOLSETS; valid values: %s", t, validToolsetsHelp)
		}
	}

	s := mcpserver.NewMCPServer(serverName, version, mcpserver.WithRecovery())
	r := &Registrar{s: s, cfg: cfg}
	for _, e := range toolsetRegistry {
		e.register(r, client)
	}
	return s, nil
}

// Add registers a tool unless policy excludes it. Filtering order:
// read-only mode (write tools) -> GITHUB_TOOLS allow-list -> GITHUB_TOOLSETS.
func (r *Registrar) Add(toolset string, writable bool, tool mcp.Tool, handler mcpserver.ToolHandlerFunc) {
	if ok, reason := toolAllowed(r.cfg, toolset, tool.GetName(), writable); !ok {
		if slices.Contains(r.cfg.Tools, tool.GetName()) {
			// The operator explicitly named this tool in GITHUB_TOOLS, yet it is
			// still excluded (toolset disabled or read-only). A plain info line is
			// too easy to miss, so escalate to warn and name the reason.
			slog.Warn("tool listed in GITHUB_TOOLS is excluded by policy",
				"tool", tool.GetName(), "toolset", toolset, "reason", reason)
		} else {
			slog.Info("skipping tool excluded by policy",
				"tool", tool.GetName(), "toolset", toolset, "reason", reason)
		}
		return
	}
	r.s.AddTool(tool, handler)
}

// toolAllowed reports whether a tool passes the configuration policy, and if
// not, why. Evaluation order matters: read-only is checked first so the
// GITHUB_TOOLS allow-list can never re-enable a write tool.
func toolAllowed(cfg *config.Config, toolset, tool string, writable bool) (bool, string) {
	if writable && cfg.ReadOnly {
		return false, "write tool disabled by READ_ONLY=true"
	}
	if len(cfg.Tools) > 0 && !slices.Contains(cfg.Tools, tool) {
		return false, "tool not in GITHUB_TOOLS allow-list"
	}
	if !toolsetEnabled(cfg, toolset) {
		return false, "toolset not enabled in GITHUB_TOOLSETS"
	}
	return true, ""
}

// toolsetEnabled reports whether a toolset is enabled by GITHUB_TOOLSETS.
// "all" enables everything; "default" enables the official default set.
func toolsetEnabled(cfg *config.Config, name string) bool {
	all := false
	def := false
	direct := false
	for _, t := range cfg.Toolsets {
		switch t {
		case "all":
			all = true
		case "default":
			def = true
		case name:
			direct = true
		}
	}
	if all || direct {
		return true
	}
	return def && defaultToolsets[name]
}
