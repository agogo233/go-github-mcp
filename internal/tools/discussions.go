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
func restCall(ctx context.Context, client *github.Client, method, path string, body, out any) (*github.Response, error) {
	req, err := client.NewRequest(method, path, body)
	if err != nil {
		return nil, err
	}
	return client.Do(ctx, req, out)
}

// repoPath escapes owner/repo before building a raw REST path.
func repoPath(owner, repo string) string {
	return url.PathEscape(owner) + "/" + url.PathEscape(repo)
}

const discussionCategoriesQuery = `
query($owner:String!, $repo:String!) {
  repository(owner:$owner, name:$repo) {
    discussionCategories(first: 50) {
      pageInfo { hasNextPage }
      nodes { id name description emoji isAnswerable }
    }
  }
}`

const repositoryIdQuery = `
query($owner:String!, $repo:String!) {
  repository(owner:$owner, name:$repo) { id }
}`

const createDiscussionMutation = `
mutation($repoId:ID!, $categoryId:ID!, $title:String!, $body:String!) {
  createDiscussion(input:{repositoryId:$repoId, categoryId:$categoryId, title:$title, body:$body}) {
    discussion { id number title url }
  }
}`

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
		if !validState(state, "open", "closed", "all") {
			return errResult("list discussions", fmt.Errorf("invalid state %q (use open, closed or all)", state))
		}
		path := fmt.Sprintf("repos/%s/discussions?state=%s&page=%d&per_page=%d",
			repoPath(owner, repo), state, page, perPage)
		var out any
		resp, err := restCall(ctx, client, "GET", path, nil, &out)
		if err != nil {
			return errResult("list discussions", err)
		}
		return jsonResultWithPage(resp, out)
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
		number, res := intIDArg(req, "discussion_number", "get discussion")
		if res != nil {
			return res, nil
		}
		var out any
		path := fmt.Sprintf("repos/%s/discussions/%d", repoPath(owner, repo), number)
		if _, err := restCall(ctx, client, "GET", path, nil, &out); err != nil {
			return errResult("get discussion", err)
		}
		return jsonResult(out)
	})

	createDiscussion := mcp.NewTool("create_discussion",
		mcp.WithDescription("Create a discussion via GraphQL. "+
			"Use list_discussion_categories first to pick a category_id (GraphQL node ID)."),
		withRepoArgs(),
		mcp.WithString("title", mcp.Required(), mcp.Description("Discussion title")),
		mcp.WithString("body", mcp.Required(), mcp.Description("Discussion body (markdown supported)")),
		mcp.WithString("category_id", mcp.Required(), mcp.Description("Discussion category GraphQL node ID, from list_discussion_categories")),
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
		categoryID, err := req.RequireString("category_id")
		if err != nil {
			return errResult("create discussion", err)
		}
		// The mutation needs the repository's GraphQL node ID; resolve it first.
		// graphqlQuery unwraps the envelope, so out receives the data payload.
		var repoOut struct {
			Repository struct {
				ID string `json:"id"`
			} `json:"repository"`
		}
		if err := graphqlQuery(ctx, client, repositoryIdQuery,
			map[string]any{"owner": owner, "repo": repo}, &repoOut); err != nil {
			return errResult("create discussion (resolve repository)", err)
		}
		if repoOut.Repository.ID == "" {
			return errResult("create discussion", fmt.Errorf("repository %s/%s not found via GraphQL", owner, repo))
		}
		var out any
		if err := graphqlQuery(ctx, client, createDiscussionMutation, map[string]any{
			"repoId":     repoOut.Repository.ID,
			"categoryId": categoryID,
			"title":      title,
			"body":       body,
		}, &out); err != nil {
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
		number, res := intIDArg(req, "discussion_number", "list discussion comments")
		if res != nil {
			return res, nil
		}
		page, perPage := pagination(req)
		path := fmt.Sprintf("repos/%s/discussions/%d/comments?page=%d&per_page=%d",
			repoPath(owner, repo), number, page, perPage)
		var out any
		resp, err := restCall(ctx, client, "GET", path, nil, &out)
		if err != nil {
			return errResult("list discussion comments", err)
		}
		return jsonResultWithPage(resp, out)
	})

	addDiscussionComment := mcp.NewTool("add_discussion_comment",
		mcp.WithDescription("Add a top-level comment to a discussion."),
		withRepoArgs(),
		mcp.WithNumber("discussion_number", mcp.Required(), mcp.Description("Discussion number")),
		mcp.WithString("body", mcp.Required(), mcp.Description("Comment body (markdown supported)")),
	)
	r.Add("discussions", true, addDiscussionComment, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		owner, repo, err := ownerRepo(req)
		if err != nil {
			return errResult("add discussion comment", err)
		}
		number, res := intIDArg(req, "discussion_number", "add discussion comment")
		if res != nil {
			return res, nil
		}
		body, err := req.RequireString("body")
		if err != nil {
			return errResult("add discussion comment", err)
		}
		payload := map[string]any{"body": body}
		path := fmt.Sprintf("repos/%s/discussions/%d/comments", repoPath(owner, repo), number)
		var out any
		if _, err := restCall(ctx, client, "POST", path, payload, &out); err != nil {
			return errResult("add discussion comment", err)
		}
		return jsonResult(out)
	})

	listDiscussionCategories := mcp.NewTool("list_discussion_categories",
		mcp.WithDescription("List discussion categories of a repository via GraphQL; "+
			"the returned id is a GraphQL node ID usable with create_discussion. "+
			"Returns at most the first 50 categories; pageInfo.hasNextPage in the result signals more exist."),
		withRepoArgs(),
	)
	r.Add("discussions", false, listDiscussionCategories, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		owner, repo, err := ownerRepo(req)
		if err != nil {
			return errResult("list discussion categories", err)
		}
		var out any
		if err := graphqlQuery(ctx, client, discussionCategoriesQuery,
			map[string]any{"owner": owner, "repo": repo}, &out); err != nil {
			return errResult("list discussion categories", err)
		}
		return jsonResult(out)
	})
}
