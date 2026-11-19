package tools

import (
	"context"
	"fmt"

	"github.com/google/go-github/v72/github"
	mcp "github.com/mark3labs/mcp-go/mcp"
)

// RegisterPullRequests registers the "pull_requests" toolset.
func RegisterPullRequests(r Registrar, client *github.Client) {
	listPRs := mcp.NewTool("list_pull_requests",
		mcp.WithDescription("List pull requests of a repository."),
		withRepoArgs(),
		mcp.WithString("state", mcp.Description("Filter: open, closed, all (default open)")),
		mcp.WithString("base", mcp.Description("Filter by base branch")),
		mcp.WithString("head", mcp.Description("Filter by head in 'user:branch' format")),
		withPagination(),
	)
	r.Add("pull_requests", false, listPRs, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		owner, repo, err := ownerRepo(req)
		if err != nil {
			return errResult("list pull requests", err)
		}
		state := req.GetString("state", "open")
		if !validState(state, "open", "closed", "all") {
			return errResult("list pull requests", fmt.Errorf("invalid state %q (use open, closed or all)", state))
		}
		opts := &github.PullRequestListOptions{
			State:       state,
			Base:        req.GetString("base", ""),
			Head:        req.GetString("head", ""),
			ListOptions: *listOptions(req),
		}
		prs, resp, err := client.PullRequests.List(ctx, owner, repo, opts)
		if err != nil {
			return errResult("list pull requests", err)
		}
		return jsonResultWithPage(resp, prs)
	})

	getPR := mcp.NewTool("get_pull_request",
		mcp.WithDescription("Get a single pull request with full metadata."),
		withRepoArgs(),
		mcp.WithNumber("pull_number", mcp.Required(), mcp.Description("Pull request number")),
	)
	r.Add("pull_requests", false, getPR, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		owner, repo, err := ownerRepo(req)
		if err != nil {
			return errResult("get pull request", err)
		}
		number, err := req.RequireInt("pull_number")
		if err != nil {
			return errResult("get pull request", err)
		}
		pr, _, err := client.PullRequests.Get(ctx, owner, repo, number)
		if err != nil {
			return errResult("get pull request", err)
		}
		return jsonResult(pr)
	})

	createPR := mcp.NewTool("create_pull_request",
		mcp.WithDescription("Create a pull request from a head branch into a base branch."),
		withRepoArgs(),
		mcp.WithString("title", mcp.Required(), mcp.Description("Pull request title")),
		mcp.WithString("head", mcp.Required(), mcp.Description("Branch containing the changes")),
		mcp.WithString("base", mcp.Required(), mcp.Description("Branch to merge into")),
		mcp.WithString("body", mcp.Description("Pull request description (markdown supported)")),
		mcp.WithBoolean("draft", mcp.Description("Create as draft pull request")),
	)
	r.Add("pull_requests", true, createPR, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		owner, repo, err := ownerRepo(req)
		if err != nil {
			return errResult("create pull request", err)
		}
		title, err := req.RequireString("title")
		if err != nil {
			return errResult("create pull request", err)
		}
		head, err := req.RequireString("head")
		if err != nil {
			return errResult("create pull request", err)
		}
		base, err := req.RequireString("base")
		if err != nil {
			return errResult("create pull request", err)
		}
		newPR := &github.NewPullRequest{
			Title: github.Ptr(title),
			Head:  github.Ptr(head),
			Base:  github.Ptr(base),
		}
		if v := req.GetString("body", ""); v != "" {
			newPR.Body = github.Ptr(v)
		}
		if req.GetBool("draft", false) {
			newPR.Draft = github.Ptr(true)
		}
		pr, _, err := client.PullRequests.Create(ctx, owner, repo, newPR)
		if err != nil {
			return errResult("create pull request", err)
		}
		return jsonResult(pr)
	})

	updatePR := mcp.NewTool("update_pull_request",
		mcp.WithDescription("Update a pull request's title, body or state."),
		withRepoArgs(),
		mcp.WithNumber("pull_number", mcp.Required(), mcp.Description("Pull request number")),
		mcp.WithString("title", mcp.Description("New title")),
		mcp.WithString("body", mcp.Description("New body; omit or pass null to keep the current body unchanged")),
		mcp.WithString("state", mcp.Description("New state: open or closed")),
	)
	r.Add("pull_requests", true, updatePR, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		owner, repo, err := ownerRepo(req)
		if err != nil {
			return errResult("update pull request", err)
		}
		number, err := req.RequireInt("pull_number")
		if err != nil {
			return errResult("update pull request", err)
		}
		patch := &github.PullRequest{}
		if v := req.GetString("title", ""); v != "" {
			patch.Title = github.Ptr(v)
		}
		if v, ok := req.GetArguments()["body"]; ok {
			if s, ok := v.(string); ok {
				patch.Body = github.Ptr(s)
			}
		}
		if v := req.GetString("state", ""); v != "" {
			if !validState(v, "open", "closed") {
				return errResult("update pull request", fmt.Errorf("invalid state %q (use open or closed)", v))
			}
			patch.State = github.Ptr(v)
		}
		pr, _, err := client.PullRequests.Edit(ctx, owner, repo, number, patch)
		if err != nil {
			return errResult("update pull request", err)
		}
		return jsonResult(pr)
	})

	mergePR := mcp.NewTool("merge_pull_request",
		mcp.WithDescription("Merge a pull request. The branch must be mergeable and required checks must pass."),
		withRepoArgs(),
		mcp.WithNumber("pull_number", mcp.Required(), mcp.Description("Pull request number")),
		mcp.WithString("commit_message", mcp.Description("Extra detail appended to the merge commit")),
		mcp.WithString("commit_title", mcp.Description("Title of the merge commit")),
		mcp.WithString("merge_method", mcp.Description("merge, squash or rebase (default merge)")),
	)
	r.Add("pull_requests", true, mergePR, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		owner, repo, err := ownerRepo(req)
		if err != nil {
			return errResult("merge pull request", err)
		}
		number, err := req.RequireInt("pull_number")
		if err != nil {
			return errResult("merge pull request", err)
		}
		opts := &github.PullRequestOptions{
			CommitTitle: req.GetString("commit_title", ""),
			MergeMethod: req.GetString("merge_method", "merge"),
		}
		result, _, err := client.PullRequests.Merge(ctx, owner, repo, number, req.GetString("commit_message", ""), opts)
		if err != nil {
			return errResult("merge pull request", err)
		}
		return jsonResult(result)
	})

	getPRFiles := mcp.NewTool("get_pull_request_files",
		mcp.WithDescription("List the files changed by a pull request with patch diffs."),
		withRepoArgs(),
		mcp.WithNumber("pull_number", mcp.Required(), mcp.Description("Pull request number")),
		withPagination(),
	)
	r.Add("pull_requests", false, getPRFiles, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		owner, repo, err := ownerRepo(req)
		if err != nil {
			return errResult("get pull request files", err)
		}
		number, err := req.RequireInt("pull_number")
		if err != nil {
			return errResult("get pull request files", err)
		}
		files, resp, err := client.PullRequests.ListFiles(ctx, owner, repo, number, listOptions(req))
		if err != nil {
			return errResult("get pull request files", err)
		}
		return jsonResultWithPage(resp, files)
	})

	getPRReviews := mcp.NewTool("get_pull_request_reviews",
		mcp.WithDescription("List reviews submitted on a pull request."),
		withRepoArgs(),
		mcp.WithNumber("pull_number", mcp.Required(), mcp.Description("Pull request number")),
		withPagination(),
	)
	r.Add("pull_requests", false, getPRReviews, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		owner, repo, err := ownerRepo(req)
		if err != nil {
			return errResult("get pull request reviews", err)
		}
		number, err := req.RequireInt("pull_number")
		if err != nil {
			return errResult("get pull request reviews", err)
		}
		reviews, resp, err := client.PullRequests.ListReviews(ctx, owner, repo, number, listOptions(req))
		if err != nil {
			return errResult("get pull request reviews", err)
		}
		return jsonResultWithPage(resp, reviews)
	})

	addPRComment := mcp.NewTool("add_pull_request_comment",
		mcp.WithDescription("Add a comment to a pull request. Without path/commit_id it posts a general "+
			"conversation comment; with them it creates a line-anchored review comment."),
		withRepoArgs(),
		mcp.WithNumber("pull_number", mcp.Required(), mcp.Description("Pull request number")),
		mcp.WithString("body", mcp.Required(), mcp.Description("Comment body (markdown supported)")),
		mcp.WithString("commit_id", mcp.Description("Commit SHA the comment anchors to (with path/line)")),
		mcp.WithString("path", mcp.Description("File path the comment anchors to")),
		mcp.WithNumber("line", mcp.Description("Line number in the diff the comment anchors to")),
	)
	r.Add("pull_requests", true, addPRComment, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		owner, repo, err := ownerRepo(req)
		if err != nil {
			return errResult("add pull request comment", err)
		}
		number, err := req.RequireInt("pull_number")
		if err != nil {
			return errResult("add pull request comment", err)
		}
		body, err := req.RequireString("body")
		if err != nil {
			return errResult("add pull request comment", err)
		}
		commitID := req.GetString("commit_id", "")
		path := req.GetString("path", "")
		if commitID != "" && path != "" {
			line := req.GetInt("line", 0)
			if line <= 0 {
				return mcp.NewToolResultError("line is required (>0) when commit_id and path are provided"), nil
			}
			comment := &github.PullRequestComment{
				Body:     github.Ptr(body),
				CommitID: github.Ptr(commitID),
				Path:     github.Ptr(path),
				Line:     github.Ptr(line),
			}
			created, _, err := client.PullRequests.CreateComment(ctx, owner, repo, number, comment)
			if err != nil {
				return errResult("add pull request comment", err)
			}
			return jsonResult(created)
		}
		// General conversation comment: pull requests are issues too.
		created, _, err := client.Issues.CreateComment(ctx, owner, repo, number, &github.IssueComment{Body: github.Ptr(body)})
		if err != nil {
			return errResult("add pull request comment", err)
		}
		return jsonResult(created)
	})

	getPRStatus := mcp.NewTool("get_pull_request_status",
		mcp.WithDescription("Get the combined CI status of a pull request's head commit."),
		withRepoArgs(),
		mcp.WithNumber("pull_number", mcp.Required(), mcp.Description("Pull request number")),
	)
	r.Add("pull_requests", false, getPRStatus, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		owner, repo, err := ownerRepo(req)
		if err != nil {
			return errResult("get pull request status", err)
		}
		number, err := req.RequireInt("pull_number")
		if err != nil {
			return errResult("get pull request status", err)
		}
		pr, _, err := client.PullRequests.Get(ctx, owner, repo, number)
		if err != nil {
			return errResult("get pull request status", err)
		}
		ref := pr.GetHead().GetSHA()
		if ref == "" {
			return mcp.NewToolResultError("pull request has no head SHA"), nil
		}
		status, _, err := client.Repositories.GetCombinedStatus(ctx, owner, repo, ref, nil)
		if err != nil {
			return errResult("get pull request status", err)
		}
		return jsonResult(status)
	})

	searchPRs := mcp.NewTool("search_pull_requests",
		mcp.WithDescription("Search pull requests with the GitHub search syntax. "+
			"'is:pr' is added automatically; do not repeat it in the query. "+
			"Scope the search with qualifiers like repo:owner/name."),
		mcp.WithString("query", mcp.Required(), mcp.Description("Search query, e.g. 'repo:owner/name is:open refactor'")),
		mcp.WithString("sort", mcp.Description("Sort field passed through to GitHub, e.g. created, updated, comments")),
		mcp.WithString("order", mcp.Description("Sort order: asc or desc")),
		withPagination(),
	)
	r.Add("pull_requests", false, searchPRs, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		q, err := req.RequireString("query")
		if err != nil {
			return errResult("search pull requests", err)
		}
		opts := &github.SearchOptions{
			Sort:        req.GetString("sort", ""),
			Order:       req.GetString("order", ""),
			ListOptions: *listOptions(req),
		}
		result, resp, err := client.Search.Issues(ctx, "is:pr "+q, opts)
		if err != nil {
			return errResult("search pull requests", err)
		}
		return jsonResultWithPage(resp, result)
	})
}
