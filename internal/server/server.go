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

// defaultToolsets is the set enabled by GITHUB_TOOLSETS=default. It mirrors
// the official github-mcp-server default set, plus the actions toolset so
// CI/CD run and job introspection works out of the box.
var defaultToolsets = map[string]bool{
	"context":       true,
	"repos":         true,
	"issues":        true,
	"pull_requests": true,
	"users":         true,
	"actions":       true,
}

// defaultTools maps individual tool names to their parent toolset. These
// tools are enabled by GITHUB_TOOLSETS=default even though their toolset is
// opt-in. compare_commits is read-only and the standard way to check how far
// a fork has diverged from upstream, so it ships in the default set.
var defaultTools = map[string]string{
	"compare_commits": "git",
}

// defaultToolEnabled reports whether an individual tool is pulled into the
// default set by name. It only applies when the "default" selector is
// present; explicit toolset lists are never widened implicitly.
func defaultToolEnabled(cfg *config.Config, toolset, tool string) bool {
	if defaultTools[tool] != toolset {
		return false
	}
	return slices.Contains(cfg.Toolsets, "default")
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

// Reason strings returned by toolAllowed. They are also used to classify
// skipped tools in the startup summary, so keep them stable: adding a reason
// here requires extending the switch in Add.
const (
	reasonReadOnly  = "write tool disabled by READ_ONLY=true"
	reasonAllowList = "tool not in GITHUB_TOOLS allow-list"
	reasonToolset   = "toolset not enabled in GITHUB_TOOLSETS"
)

// Registrar wraps the MCP server and applies configuration policy
// (toolset allow-list and read-only mode) before tools are registered.
type Registrar struct {
	s   *mcpserver.MCPServer
	cfg *config.Config

	registered       int
	skippedReadOnly  int
	skippedAllowList int
	skippedToolset   int
}

// New builds the MCP server with every configured tool registered. It fails
// fast when GITHUB_TOOLSETS contains an unknown toolset name.
func New(cfg *config.Config, client *github.Client, version string) (*mcpserver.MCPServer, error) {
	for _, t := range cfg.Toolsets {
		if !knownToolsets[t] {
			return nil, fmt.Errorf("unknown toolset %q in GITHUB_TOOLSETS; valid values: %s", t, validToolsetsHelp)
		}
	}

	s := mcpserver.NewMCPServer(serverName, version,
		mcpserver.WithRecovery(),
		mcpserver.WithInstructions(buildInstructions(cfg)),
	)
	r := &Registrar{s: s, cfg: cfg}
	for _, e := range toolsetRegistry {
		e.register(r, client)
	}
	r.logSummary()
	return s, nil
}

// buildInstructions returns the usage guidance served to agents in the
// initialize response. It states the operational mode and the conventions
// shared by all tools so agents can act without trial and error.
func buildInstructions(cfg *config.Config) string {
	mode := "Write tools are available on this deployment (READ_ONLY is not set)."
	if cfg.ReadOnly {
		mode = "This deployment runs in READ-ONLY mode: write tools are not registered, so write tool calls are rejected as unknown tools; use read tools only."
	}
	return strings.Join([]string{
		"go-github-mcp exposes GitHub REST operations as MCP tools.",
		"",
		"Getting started:",
		"- Call get_me first to verify authentication and see which account this server acts as.",
		"- Repository tools take owner and repo as separate required parameters: owner is the user or organization login, repo is the repository name.",
		"- List tools paginate with page (starts at 1) and per_page (default 30, max 100); omit both for the first page.",
		"- List results may append a second text block with pagination metadata (has_next/next_page); check it before fetching the next page.",
		"- Tool results carry JSON as text content; parse that text, not the wrapper.",
		"- On failure a tool returns isError=true with a message describing the GitHub API error; read that message before retrying.",
		"",
		"Mode: " + mode,
		"Which toolsets are enabled depends on this deployment's configuration; treat the tool list as the source of truth for what exists and which parameters are required.",
	}, "\n")
}

// logSummary emits one line describing the registration outcome, replacing
// the former per-tool skip log flood. Per-tool detail remains available at
// debug level via Add.
func (r *Registrar) logSummary() {
	slog.Info("tool registration complete",
		"registered", r.registered,
		"skipped_read_only", r.skippedReadOnly,
		"skipped_toolset_disabled", r.skippedToolset,
		"skipped_allow_list", r.skippedAllowList,
	)
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
			// Default output stays quiet: only a debug line per skipped tool
			// here, plus the single summary line from logSummary at the end.
			slog.Debug("skipping tool excluded by policy",
				"tool", tool.GetName(), "toolset", toolset, "reason", reason)
		}
		switch reason {
		case reasonReadOnly:
			r.skippedReadOnly++
		case reasonAllowList:
			r.skippedAllowList++
		case reasonToolset:
			r.skippedToolset++
		}
		return
	}
	r.registered++
	r.s.AddTool(tool, handler)
}

// toolAllowed reports whether a tool passes the configuration policy, and if
// not, why. Evaluation order matters: read-only is checked first so the
// GITHUB_TOOLS allow-list can never re-enable a write tool.
func toolAllowed(cfg *config.Config, toolset, tool string, writable bool) (bool, string) {
	if writable && cfg.ReadOnly {
		return false, reasonReadOnly
	}
	if len(cfg.Tools) > 0 && !slices.Contains(cfg.Tools, tool) {
		return false, reasonAllowList
	}
	if !toolsetEnabled(cfg, toolset) && !defaultToolEnabled(cfg, toolset, tool) {
		return false, reasonToolset
	}
	return true, ""
}

// toolsetEnabled reports whether a toolset is enabled by GITHUB_TOOLSETS.
// "all" enables everything; "default" enables the curated default set in
// defaultToolsets, plus the individual tools pulled in via defaultTools.
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
