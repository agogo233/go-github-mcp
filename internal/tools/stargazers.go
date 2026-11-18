package tools

import (
	"context"

	"github.com/google/go-github/v72/github"
	mcp "github.com/mark3labs/mcp-go/mcp"
)

// RegisterStargazers registers the "stargazers" toolset.
func RegisterStargazers(r Registrar, client *github.Client) {
	listStargazers := mcp.NewTool("list_stargazers",
		mcp.WithDescription("List users who starred a repository."),
		mcp.WithString("owner", mcp.Required(), mcp.Description("Repository owner")),
		mcp.WithString("repo", mcp.Required(), mcp.Description("Repository name")),
		mcp.WithNumber("page", mcp.Description("Page number (starts at 1)")),
		mcp.WithNumber("per_page", mcp.Description("Results per page (default 30, max 100)")),
	)
	r.Add("stargazers", false, listStargazers, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		owner, repo, err := ownerRepo(req)
		if err != nil {
			return errResult("list stargazers", err)
		}
		stargazers, _, err := client.Activity.ListStargazers(ctx, owner, repo, listOptions(req))
		if err != nil {
			return errResult("list stargazers", err)
		}
		return jsonResult(stargazers)
	})

	listStarredRepos := mcp.NewTool("list_starred_repos",
		mcp.WithDescription("List repositories starred by the authenticated user (or a given username)."),
		mcp.WithString("username", mcp.Description("Username; empty means the authenticated user")),
		mcp.WithString("sort", mcp.Description("created or updated (default created)")),
		mcp.WithNumber("page", mcp.Description("Page number (starts at 1)")),
		mcp.WithNumber("per_page", mcp.Description("Results per page (default 30, max 100)")),
	)
	r.Add("stargazers", false, listStarredRepos, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		opts := &github.ActivityListStarredOptions{
			Sort: req.GetString("sort", ""),
			ListOptions: func() github.ListOptions {
				page, perPage := pagination(req)
				return github.ListOptions{Page: page, PerPage: perPage}
			}(),
		}
		starred, _, err := client.Activity.ListStarred(ctx, req.GetString("username", ""), opts)
		if err != nil {
			return errResult("list starred repos", err)
		}
		return jsonResult(starred)
	})
}
