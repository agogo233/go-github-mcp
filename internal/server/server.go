// Package server assembles the MCP server, applies toolset filtering and
// read-only policy, and registers all tool groups.
package server

import (
	"log/slog"
	"slices"

	"github.com/google/go-github/v72/github"
	mcp "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/agogo233/go-github-mcp/internal/config"
	"github.com/agogo233/go-github-mcp/internal/tools"
)

const serverName = "go-github-mcp"

// defaultToolsets mirrors the official github-mcp-server default toolset set.
var defaultToolsets = map[string]bool{
	"context":       true,
	"repos":         true,
	"issues":        true,
	"pull_requests": true,
	"users":         true,
}

// Registrar wraps the MCP server and applies configuration policy
// (toolset allow-list and read-only mode) before tools are registered.
type Registrar struct {
	s   *mcpserver.MCPServer
	cfg *config.Config
}

// New builds the MCP server with every configured tool registered.
func New(cfg *config.Config, client *github.Client, version string) *mcpserver.MCPServer {
	s := mcpserver.NewMCPServer(serverName, version, mcpserver.WithRecovery())
	r := &Registrar{s: s, cfg: cfg}

	tools.RegisterContext(r, client)
	// Future toolsets (repos, issues, pull_requests, users, ...) are
	// registered here in later milestones.

	return s
}

// Add registers a tool unless policy excludes it. Filtering order:
// read-only mode (write tools) -> GITHUB_TOOLS allow-list -> GITHUB_TOOLSETS.
func (r *Registrar) Add(toolset string, writable bool, tool mcp.Tool, handler mcpserver.ToolHandlerFunc) {
	if writable && r.cfg.ReadOnly {
		slog.Info("skipping write tool in read-only mode", "tool", tool.GetName(), "toolset", toolset)
		return
	}
	if len(r.cfg.Tools) > 0 && !slices.Contains(r.cfg.Tools, tool.GetName()) {
		slog.Info("skipping tool not in GITHUB_TOOLS allow-list", "tool", tool.GetName(), "toolset", toolset)
		return
	}
	if !r.toolsetEnabled(toolset) {
		slog.Info("skipping tool from disabled toolset", "tool", tool.GetName(), "toolset", toolset)
		return
	}
	r.s.AddTool(tool, handler)
}

// toolsetEnabled reports whether a toolset is enabled by GITHUB_TOOLSETS.
// "all" enables everything; "default" enables the official default set.
func (r *Registrar) toolsetEnabled(name string) bool {
	all := false
	def := false
	direct := false
	for _, t := range r.cfg.Toolsets {
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
