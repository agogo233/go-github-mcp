// Package tools implements MCP tools backed by the GitHub API,
// grouped by toolset. Each group exposes a single Register function.
package tools

import (
	"context"

	"github.com/google/go-github/v72/github"
	mcp "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
)

// Registrar is implemented by the server package to register tools under
// policy control (toolset allow-list, read-only mode). Defined here to avoid
// an import cycle between the tools and server packages.
type Registrar interface {
	Add(toolset string, writable bool, tool mcp.Tool, handler mcpserver.ToolHandlerFunc)
}

// RegisterContext registers the "context" toolset: tools that describe the
// authenticated account the server is operating as.
func RegisterContext(r Registrar, client *github.Client) {
	getMe := mcp.NewTool("get_me",
		mcp.WithDescription(
			"Get details of the authenticated GitHub user that backs this server. "+
				"Use this first to learn which account and permissions are in effect."),
	)

	r.Add("context", false, getMe, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		user, _, err := client.Users.Get(ctx, "")
		if err != nil {
			return errResult("get authenticated user", err)
		}
		return jsonResult(user)
	})
}
