package tools

import (
	"context"
	"strings"
	"time"

	"github.com/google/go-github/v72/github"
	mcp "github.com/mark3labs/mcp-go/mcp"
)

// RegisterIssues registers the "issues" toolset.
func RegisterIssues(r Registrar, client *github.Client) {
	listIssues := mcp.NewTool("list_issues",
		mcp.WithDescription("List issues of a repository (excludes pull requests)."),
		mcp.WithString("owner", mcp.Required(), mcp.Description("Repository owner")),
		mcp.WithString("repo", mcp.Required(), mcp.Description("Repository name")),
		mcp.WithString("state", mcp.Description("Filter: open, closed, all (default open)")),
		mcp.WithString("labels", mcp.Description("Comma-separated label names to filter by")),
		mcp.WithString("assignee", mcp.Description("Filter by assignee, '*' for any, 'none' for unassigned")),
		mcp.WithString("since", mcp.Description("Only issues updated at or after this ISO 8601 timestamp")),
		mcp.WithNumber("page", mcp.Description("Page number (starts at 1)")),
		mcp.WithNumber("per_page", mcp.Description("Results per page (default 30, max 100)")),
	)
	r.Add("issues", false, listIssues, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		owner, repo, err := ownerRepo(req)
		if err != nil {
			return errResult("list issues", err)
		}
		opts := &github.IssueListByRepoOptions{
			State:    req.GetString("state", "open"),
			Assignee: req.GetString("assignee", ""),
			Labels:   splitCommaList(req.GetString("labels", "")),
			ListOptions: func() github.ListOptions {
				page, perPage := pagination(req)
				return github.ListOptions{Page: page, PerPage: perPage}
			}(),
		}
		if v := req.GetString("since", ""); v != "" {
			t, err := time.Parse(time.RFC3339, v)
			if err != nil {
				return errResult("list issues (since must be RFC3339, e.g. 2026-01-02T15:04:05Z)", err)
			}
			opts.Since = t
		}
		issues, _, err := client.Issues.ListByRepo(ctx, owner, repo, opts)
		if err != nil {
			return errResult("list issues", err)
		}
		return jsonResult(issues)
	})

	getIssue := mcp.NewTool("get_issue",
		mcp.WithDescription("Get a single issue with its metadata."),
		mcp.WithString("owner", mcp.Required(), mcp.Description("Repository owner")),
		mcp.WithString("repo", mcp.Required(), mcp.Description("Repository name")),
		mcp.WithNumber("issue_number", mcp.Required(), mcp.Description("Issue number")),
	)
	r.Add("issues", false, getIssue, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		owner, repo, err := ownerRepo(req)
		if err != nil {
			return errResult("get issue", err)
		}
		number, err := req.RequireInt("issue_number")
		if err != nil {
			return errResult("get issue", err)
		}
		issue, _, err := client.Issues.Get(ctx, owner, repo, number)
		if err != nil {
			return errResult("get issue", err)
		}
		return jsonResult(issue)
	})

	createIssue := mcp.NewTool("create_issue",
		mcp.WithDescription("Create a new issue in a repository."),
		mcp.WithString("owner", mcp.Required(), mcp.Description("Repository owner")),
		mcp.WithString("repo", mcp.Required(), mcp.Description("Repository name")),
		mcp.WithString("title", mcp.Required(), mcp.Description("Issue title")),
		mcp.WithString("body", mcp.Description("Issue body (markdown supported)")),
		mcp.WithArray("labels",
			mcp.Description("Label names to apply"),
			mcp.Items(map[string]any{"type": "string"}),
		),
		mcp.WithString("assignee", mcp.Description("Single username to assign")),
	)
	r.Add("issues", true, createIssue, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		owner, repo, err := ownerRepo(req)
		if err != nil {
			return errResult("create issue", err)
		}
		title, err := req.RequireString("title")
		if err != nil {
			return errResult("create issue", err)
		}
		issueReq := &github.IssueRequest{Title: github.Ptr(title)}
		if v := req.GetString("body", ""); v != "" {
			issueReq.Body = github.Ptr(v)
		}
		if labels := req.GetStringSlice("labels", nil); len(labels) > 0 {
			issueReq.Labels = &labels
		}
		if v := req.GetString("assignee", ""); v != "" {
			issueReq.Assignee = github.Ptr(v)
		}
		issue, _, err := client.Issues.Create(ctx, owner, repo, issueReq)
		if err != nil {
			return errResult("create issue", err)
		}
		return jsonResult(issue)
	})

	updateIssue := mcp.NewTool("update_issue",
		mcp.WithDescription("Update an existing issue: title, body, state (open/closed), labels or assignee."),
		mcp.WithString("owner", mcp.Required(), mcp.Description("Repository owner")),
		mcp.WithString("repo", mcp.Required(), mcp.Description("Repository name")),
		mcp.WithNumber("issue_number", mcp.Required(), mcp.Description("Issue number")),
		mcp.WithString("title", mcp.Description("New title")),
		mcp.WithString("body", mcp.Description("New body")),
		mcp.WithString("state", mcp.Description("New state: open or closed")),
		mcp.WithString("state_reason", mcp.Description("Reason for the state change: completed, not_planned, reopened")),
		mcp.WithArray("labels",
			mcp.Description("Replace existing labels with this list"),
			mcp.Items(map[string]any{"type": "string"}),
		),
	)
	r.Add("issues", true, updateIssue, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		owner, repo, err := ownerRepo(req)
		if err != nil {
			return errResult("update issue", err)
		}
		number, err := req.RequireInt("issue_number")
		if err != nil {
			return errResult("update issue", err)
		}
		issueReq := &github.IssueRequest{}
		if v := req.GetString("title", ""); v != "" {
			issueReq.Title = github.Ptr(v)
		}
		if v, ok := req.GetArguments()["body"]; ok {
			if s, ok := v.(string); ok {
				issueReq.Body = github.Ptr(s)
			}
		}
		if v := req.GetString("state", ""); v != "" {
			issueReq.State = github.Ptr(v)
		}
		if v := req.GetString("state_reason", ""); v != "" {
			issueReq.StateReason = github.Ptr(v)
		}
		if labels, ok := req.GetArguments()["labels"]; ok {
			// Presence matters: an empty array explicitly clears all labels.
			if arr, ok := labels.([]any); ok {
				ls := make([]string, 0, len(arr))
				for _, item := range arr {
					if s, ok := item.(string); ok {
						ls = append(ls, s)
					}
				}
				issueReq.Labels = &ls
			}
		}
		issue, _, err := client.Issues.Edit(ctx, owner, repo, number, issueReq)
		if err != nil {
			return errResult("update issue", err)
		}
		return jsonResult(issue)
	})

	addIssueComment := mcp.NewTool("add_issue_comment",
		mcp.WithDescription("Add a comment to an issue."),
		mcp.WithString("owner", mcp.Required(), mcp.Description("Repository owner")),
		mcp.WithString("repo", mcp.Required(), mcp.Description("Repository name")),
		mcp.WithNumber("issue_number", mcp.Required(), mcp.Description("Issue number")),
		mcp.WithString("body", mcp.Required(), mcp.Description("Comment body (markdown supported)")),
	)
	r.Add("issues", true, addIssueComment, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		owner, repo, err := ownerRepo(req)
		if err != nil {
			return errResult("add issue comment", err)
		}
		number, err := req.RequireInt("issue_number")
		if err != nil {
			return errResult("add issue comment", err)
		}
		body, err := req.RequireString("body")
		if err != nil {
			return errResult("add issue comment", err)
		}
		comment, _, err := client.Issues.CreateComment(ctx, owner, repo, number, &github.IssueComment{Body: github.Ptr(body)})
		if err != nil {
			return errResult("add issue comment", err)
		}
		return jsonResult(comment)
	})

	listIssueComments := mcp.NewTool("list_issue_comments",
		mcp.WithDescription("List comments of an issue in chronological order."),
		mcp.WithString("owner", mcp.Required(), mcp.Description("Repository owner")),
		mcp.WithString("repo", mcp.Required(), mcp.Description("Repository name")),
		mcp.WithNumber("issue_number", mcp.Required(), mcp.Description("Issue number")),
		mcp.WithNumber("page", mcp.Description("Page number (starts at 1)")),
		mcp.WithNumber("per_page", mcp.Description("Results per page (default 30, max 100)")),
	)
	r.Add("issues", false, listIssueComments, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		owner, repo, err := ownerRepo(req)
		if err != nil {
			return errResult("list issue comments", err)
		}
		number, err := req.RequireInt("issue_number")
		if err != nil {
			return errResult("list issue comments", err)
		}
		comments, _, err := client.Issues.ListComments(ctx, owner, repo, number, &github.IssueListCommentsOptions{
			ListOptions: *listOptions(req),
		})
		if err != nil {
			return errResult("list issue comments", err)
		}
		return jsonResult(comments)
	})
}

// splitCommaList parses a comma-separated argument into a clean slice.
func splitCommaList(v string) []string {
	if v == "" {
		return nil
	}
	var out []string
	for _, item := range strings.Split(v, ",") {
		if s := strings.TrimSpace(item); s != "" {
			out = append(out, s)
		}
	}
	return out
}
