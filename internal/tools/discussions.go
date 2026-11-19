package tools

import (
	"context"
	"fmt"
	"net/url"

	"github.com/google/go-github/v72/github"
	mcp "github.com/mark3labs/mcp-go/mcp"
)

// Discussions are not covered by go-github's typed services yet, so these
// tools call the REST endpoints directly through the shared client (which
// keeps GHES base-URL handling and authentication identical).
func restCall(ctx context.Context, client *github.Client, method, path string, body, out any) error {
	req, err := client.NewRequest(method, path, body)
	if err != nil {
		return err
	}
	_, err = client.Do(ctx, req, out)
	return err
}

// repoPath escapes owner/repo before building a raw REST path.
func repoPath(owner, repo string) string {
	return url.PathEscape(owner) + "/" + url.PathEscape(repo)
}

// RegisterDiscussions registers the "discussions" toolset.
func RegisterDiscussions(r Registrar, client *github.Client) {
	listDiscussions := mcp.NewTool("list_discussions",
		mcp.WithDescription("List discussions of a repository."),
		withRepoArgs(),
		mcp.WithString("state", mcp.Description("Filter: open, closed, all (default all)")),
		withPagination(),
	)
	r.Add("discussions", false, listDiscussions, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		owner, repo, err := ownerRepo(req)
		if err != nil {
			return errResult("list discussions", err)
		}
		page, perPage := pagination(req)
		// Whitelist state before interpolating into the query string so a
		// hostile value cannot inject extra query parameters.
		state := req.GetString("state", "all")
		switch state {
		case "open", "closed", "all":
		default:
			return errResult("list discussions", fmt.Errorf("invalid state %q (use open, closed or all)", state))
		}
		path := fmt.Sprintf("repos/%s/discussions?state=%s&page=%d&per_page=%d",
			repoPath(owner, repo), state, page, perPage)
		var out any
		if err := restCall(ctx, client, "GET", path, nil, &out); err != nil {
			return errResult("list discussions", err)
		}
		return jsonResult(out)
	})

	getDiscussion := mcp.NewTool("get_discussion",
		mcp.WithDescription("Get a single discussion by number."),
		withRepoArgs(),
		mcp.WithNumber("discussion_number", mcp.Required(), mcp.Description("Discussion number")),
	)
	r.Add("discussions", false, getDiscussion, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		owner, repo, err := ownerRepo(req)
		if err != nil {
			return errResult("get discussion", err)
		}
		number, err := req.RequireInt("discussion_number")
		if err != nil {
			return errResult("get discussion", err)
		}
		var out any
		path := fmt.Sprintf("repos/%s/discussions/%d", repoPath(owner, repo), number)
		if err := restCall(ctx, client, "GET", path, nil, &out); err != nil {
			return errResult("get discussion", err)
		}
		return jsonResult(out)
	})

	createDiscussion := mcp.NewTool("create_discussion",
		mcp.WithDescription("Create a discussion. Use list_discussion_categories first to pick a category_id."),
		withRepoArgs(),
		mcp.WithString("title", mcp.Required(), mcp.Description("Discussion title")),
		mcp.WithString("body", mcp.Required(), mcp.Description("Discussion body (markdown supported)")),
		mcp.WithNumber("category_id", mcp.Required(), mcp.Description("Discussion category ID (integer REST id, from the categories endpoint)")),
	)
	r.Add("discussions", true, createDiscussion, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		owner, repo, err := ownerRepo(req)
		if err != nil {
			return errResult("create discussion", err)
		}
		title, err := req.RequireString("title")
		if err != nil {
			return errResult("create discussion", err)
		}
		body, err := req.RequireString("body")
		if err != nil {
			return errResult("create discussion", err)
		}
		categoryID, err := req.RequireInt("category_id")
		if err != nil {
			return errResult("create discussion", err)
		}
		payload := map[string]any{
			"title":       title,
			"body":        body,
			"category_id": categoryID,
		}
		var out any
		path := fmt.Sprintf("repos/%s/discussions", repoPath(owner, repo))
		if err := restCall(ctx, client, "POST", path, payload, &out); err != nil {
			return errResult("create discussion", err)
		}
		return jsonResult(out)
	})

	listDiscussionComments := mcp.NewTool("list_discussion_comments",
		mcp.WithDescription("List comments of a discussion."),
		withRepoArgs(),
		mcp.WithNumber("discussion_number", mcp.Required(), mcp.Description("Discussion number")),
		withPagination(),
	)
	r.Add("discussions", false, listDiscussionComments, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		owner, repo, err := ownerRepo(req)
		if err != nil {
			return errResult("list discussion comments", err)
		}
		number, err := req.RequireInt("discussion_number")
		if err != nil {
			return errResult("list discussion comments", err)
		}
		page, perPage := pagination(req)
		path := fmt.Sprintf("repos/%s/discussions/%d/comments?page=%d&per_page=%d",
			repoPath(owner, repo), number, page, perPage)
		var out any
		if err := restCall(ctx, client, "GET", path, nil, &out); err != nil {
			return errResult("list discussion comments", err)
		}
		return jsonResult(out)
	})
}
