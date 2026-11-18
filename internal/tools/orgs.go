package tools

import (
	"context"

	"github.com/google/go-github/v72/github"
	mcp "github.com/mark3labs/mcp-go/mcp"
)

// RegisterOrgs registers the "orgs" toolset.
func RegisterOrgs(r Registrar, client *github.Client) {
	getOrg := mcp.NewTool("get_organization",
		mcp.WithDescription("Get details of a GitHub organization."),
		mcp.WithString("org", mcp.Required(), mcp.Description("Organization login")),
	)
	r.Add("orgs", false, getOrg, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		org, err := req.RequireString("org")
		if err != nil {
			return errResult("get organization", err)
		}
		organization, _, err := client.Organizations.Get(ctx, org)
		if err != nil {
			return errResult("get organization", err)
		}
		return jsonResult(organization)
	})

	listOrgMembers := mcp.NewTool("list_org_members",
		mcp.WithDescription("List members of an organization."),
		mcp.WithString("org", mcp.Required(), mcp.Description("Organization login")),
		mcp.WithString("filter", mcp.Description("2fa_disabled or all (default all)")),
		mcp.WithString("role", mcp.Description("all, admin or member (default all)")),
		mcp.WithNumber("page", mcp.Description("Page number (starts at 1)")),
		mcp.WithNumber("per_page", mcp.Description("Results per page (default 30, max 100)")),
	)
	r.Add("orgs", false, listOrgMembers, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		org, err := req.RequireString("org")
		if err != nil {
			return errResult("list org members", err)
		}
		opts := &github.ListMembersOptions{
			Filter: req.GetString("filter", ""),
			Role:   req.GetString("role", ""),
			ListOptions: func() github.ListOptions {
				page, perPage := pagination(req)
				return github.ListOptions{Page: page, PerPage: perPage}
			}(),
		}
		members, _, err := client.Organizations.ListMembers(ctx, org, opts)
		if err != nil {
			return errResult("list org members", err)
		}
		return jsonResult(members)
	})

	listOrgTeams := mcp.NewTool("list_org_teams",
		mcp.WithDescription("List teams of an organization."),
		mcp.WithString("org", mcp.Required(), mcp.Description("Organization login")),
		mcp.WithNumber("page", mcp.Description("Page number (starts at 1)")),
		mcp.WithNumber("per_page", mcp.Description("Results per page (default 30, max 100)")),
	)
	r.Add("orgs", false, listOrgTeams, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		org, err := req.RequireString("org")
		if err != nil {
			return errResult("list org teams", err)
		}
		teams, _, err := client.Teams.ListTeams(ctx, org, listOptions(req))
		if err != nil {
			return errResult("list org teams", err)
		}
		return jsonResult(teams)
	})

	listOrgRepos := mcp.NewTool("list_org_repositories",
		mcp.WithDescription("List repositories belonging to an organization."),
		mcp.WithString("org", mcp.Required(), mcp.Description("Organization login")),
		mcp.WithString("type", mcp.Description("all, public, private, forks, sources, member (default all)")),
		mcp.WithString("sort", mcp.Description("created, updated, pushed, full_name (default full_name)")),
		mcp.WithNumber("page", mcp.Description("Page number (starts at 1)")),
		mcp.WithNumber("per_page", mcp.Description("Results per page (default 30, max 100)")),
	)
	r.Add("orgs", false, listOrgRepos, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		org, err := req.RequireString("org")
		if err != nil {
			return errResult("list org repositories", err)
		}
		opts := &github.RepositoryListByOrgOptions{
			Type: req.GetString("type", ""),
			Sort: req.GetString("sort", ""),
			ListOptions: func() github.ListOptions {
				page, perPage := pagination(req)
				return github.ListOptions{Page: page, PerPage: perPage}
			}(),
		}
		repos, _, err := client.Repositories.ListByOrg(ctx, org, opts)
		if err != nil {
			return errResult("list org repositories", err)
		}
		return jsonResult(repos)
	})
}
