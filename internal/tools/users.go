package tools

import (
	"context"

	"github.com/google/go-github/v72/github"
	mcp "github.com/mark3labs/mcp-go/mcp"
)

// RegisterUsers registers the "users" toolset.
func RegisterUsers(r Registrar, client *github.Client) {
	getUser := mcp.NewTool("get_user",
		mcp.WithDescription("Get details for a GitHub user. Omit username to get the authenticated user."),
		mcp.WithString("username", mcp.Description("Username to look up; empty means the authenticated user")),
	)
	r.Add("users", false, getUser, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		user, _, err := client.Users.Get(ctx, req.GetString("username", ""))
		if err != nil {
			return errResult("get user", err)
		}
		return jsonResult(user)
	})

	searchUsers := mcp.NewTool("search_users",
		mcp.WithDescription("Search for GitHub users by keyword (login, name, email)."),
		mcp.WithString("query", mcp.Required(), mcp.Description("Search query, e.g. 'location:china language:go'")),
		withPagination(),
	)
	r.Add("users", false, searchUsers, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		q, err := req.RequireString("query")
		if err != nil {
			return errResult("search users", err)
		}
		opts := &github.SearchOptions{ListOptions: *listOptions(req)}
		result, resp, err := client.Search.Users(ctx, q, opts)
		if err != nil {
			return errResult("search users", err)
		}
		return jsonResultWithPage(resp, result)
	})
}
