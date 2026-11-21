package tools

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/google/go-github/v72/github"
	mcp "github.com/mark3labs/mcp-go/mcp"
)

// RegisterIssues registers the "issues" toolset.
func RegisterIssues(r Registrar, client *github.Client) {
	listIssues := mcp.NewTool("list_issues",
		mcp.WithDescription("List issues of a repository (excludes pull requests)."),
		withRepoArgs(),
		mcp.WithString("state", mcp.Description("Filter: open, closed, all (default open)")),
		mcp.WithString("labels", mcp.Description("Comma-separated label names to filter by")),
		mcp.WithString("assignee", mcp.Description("Filter by assignee, '*' for any, 'none' for unassigned")),
		mcp.WithString("since", mcp.Description("Only issues updated at or after this ISO 8601 timestamp")),
		withPagination(),
	)
	r.Add("issues", false, listIssues, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		owner, repo, err := ownerRepo(req)
		if err != nil {
			return errResult("list issues", err)
		}
		state := req.GetString("state", "open")
		if !validState(state, "open", "closed", "all") {
			return errResult("list issues", fmt.Errorf("invalid state %q (use open, closed or all)", state))
		}
		opts := &github.IssueListByRepoOptions{
			State:       state,
			Assignee:    req.GetString("assignee", ""),
			Labels:      splitCommaList(req.GetString("labels", "")),
			ListOptions: *listOptions(req),
		}
		if v := req.GetString("since", ""); v != "" {
			t, err := time.Parse(time.RFC3339, v)
			if err != nil {
				return errResult("list issues (since must be RFC3339, e.g. 2026-01-02T15:04:05Z)", err)
			}
			opts.Since = t
		}
		issues, resp, err := client.Issues.ListByRepo(ctx, owner, repo, opts)
		if err != nil {
			return errResult("list issues", err)
		}
		return jsonResultWithPage(resp, issues)
	})

	getIssue := mcp.NewTool("get_issue",
		mcp.WithDescription("Get a single issue with its metadata."),
		withRepoArgs(),
		mcp.WithNumber("issue_number", mcp.Required(), mcp.Description("Issue number")),
	)
	r.Add("issues", false, getIssue, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		owner, repo, err := ownerRepo(req)
		if err != nil {
			return errResult("get issue", err)
		}
		number, res := intIDArg(req, "issue_number", "get issue")
		if res != nil {
			return res, nil
		}
		issue, _, err := client.Issues.Get(ctx, owner, repo, number)
		if err != nil {
			return errResult("get issue", err)
		}
		return jsonResult(issue)
	})

	createIssue := mcp.NewTool("create_issue",
		mcp.WithDescription("Create a new issue in a repository."),
		withRepoArgs(),
		mcp.WithString("title", mcp.Required(), mcp.Description("Issue title")),
		mcp.WithString("body", mcp.Description("Issue body (markdown supported)")),
		mcp.WithArray("labels",
			mcp.Description("Label names to apply"),
			mcp.Items(map[string]any{"type": "string"}),
		),
		mcp.WithString("assignee", mcp.Description("Single username to assign (legacy; prefer assignees)")),
		mcp.WithArray("assignees",
			mcp.Description("Usernames to assign"),
			mcp.Items(map[string]any{"type": "string"}),
		),
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
		if assignees := req.GetStringSlice("assignees", nil); len(assignees) > 0 {
			issueReq.Assignees = &assignees
		}
		issue, _, err := client.Issues.Create(ctx, owner, repo, issueReq)
		if err != nil {
			return errResult("create issue", err)
		}
		return jsonResult(issue)
	})

	updateIssue := mcp.NewTool("update_issue",
		mcp.WithDescription("Update an existing issue: title, body, state (open/closed), labels, assignees or milestone."),
		withRepoArgs(),
		mcp.WithNumber("issue_number", mcp.Required(), mcp.Description("Issue number")),
		mcp.WithString("title", mcp.Description("New title")),
		mcp.WithString("body", mcp.Description("New body; omit or pass null to keep the current body unchanged")),
		mcp.WithString("state", mcp.Description("New state: open or closed")),
		mcp.WithString("state_reason", mcp.Description("Reason for the state change: completed, not_planned, reopened")),
		mcp.WithArray("labels",
			mcp.Description("Replace existing labels with this list"),
			mcp.Items(map[string]any{"type": "string"}),
		),
		mcp.WithArray("assignees",
			mcp.Description("Replace existing assignees with this list; an empty array clears all assignees"),
			mcp.Items(map[string]any{"type": "string"}),
		),
		mcp.WithNumber("milestone", mcp.Description("Milestone number to set; clearing an existing milestone is not supported by this tool")),
	)
	r.Add("issues", true, updateIssue, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		owner, repo, err := ownerRepo(req)
		if err != nil {
			return errResult("update issue", err)
		}
		number, res := intIDArg(req, "issue_number", "update issue")
		if res != nil {
			return res, nil
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
			if !validState(v, "open", "closed") {
				return errResult("update issue", fmt.Errorf("invalid state %q (use open or closed)", v))
			}
			issueReq.State = github.Ptr(v)
		}
		if v := req.GetString("state_reason", ""); v != "" {
			if !validState(v, "completed", "not_planned", "reopened") {
				return errResult("update issue", fmt.Errorf("invalid state_reason %q (use completed, not_planned or reopened)", v))
			}
			issueReq.StateReason = github.Ptr(v)
		}
		if labels, ok := req.GetArguments()["labels"]; ok {
			// Presence matters: an empty array explicitly clears all labels.
			if ls, ok := stringListArg(labels); ok {
				issueReq.Labels = &ls
			}
		}
		if assignees, ok := req.GetArguments()["assignees"]; ok {
			// Presence matters: an empty array explicitly clears all assignees.
			if as, ok := stringListArg(assignees); ok {
				issueReq.Assignees = &as
			}
		}
		if v, ok := req.GetArguments()["milestone"]; ok {
			f, isNum := v.(float64)
			if !isNum || f < 1 || f > math.MaxInt32 || f != math.Trunc(f) {
				return errResult("update issue", fmt.Errorf("milestone must be a positive milestone number; clearing a milestone is not supported"))
			}
			m := int(f)
			issueReq.Milestone = &m
		}
		issue, _, err := client.Issues.Edit(ctx, owner, repo, number, issueReq)
		if err != nil {
			return errResult("update issue", err)
		}
		return jsonResult(issue)
	})

	addIssueComment := mcp.NewTool("add_issue_comment",
		mcp.WithDescription("Add a comment to an issue."),
		withRepoArgs(),
		mcp.WithNumber("issue_number", mcp.Required(), mcp.Description("Issue number")),
		mcp.WithString("body", mcp.Required(), mcp.Description("Comment body (markdown supported)")),
	)
	r.Add("issues", true, addIssueComment, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		owner, repo, err := ownerRepo(req)
		if err != nil {
			return errResult("add issue comment", err)
		}
		number, res := intIDArg(req, "issue_number", "add issue comment")
		if res != nil {
			return res, nil
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
		withRepoArgs(),
		mcp.WithNumber("issue_number", mcp.Required(), mcp.Description("Issue number")),
		withPagination(),
	)
	r.Add("issues", false, listIssueComments, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		owner, repo, err := ownerRepo(req)
		if err != nil {
			return errResult("list issue comments", err)
		}
		number, res := intIDArg(req, "issue_number", "list issue comments")
		if res != nil {
			return res, nil
		}
		comments, resp, err := client.Issues.ListComments(ctx, owner, repo, number, &github.IssueListCommentsOptions{
			ListOptions: *listOptions(req),
		})
		if err != nil {
			return errResult("list issue comments", err)
		}
		return jsonResultWithPage(resp, comments)
	})

	searchIssues := mcp.NewTool("search_issues",
		mcp.WithDescription("Search issues across GitHub with the GitHub search syntax. "+
			"Scope the search with qualifiers like repo:owner/name; results may also match pull requests."),
		mcp.WithString("query", mcp.Required(), mcp.Description("Search query, e.g. 'repo:owner/name is:open bug'")),
		mcp.WithString("sort", mcp.Description("Sort field passed through to GitHub, e.g. created, updated, comments")),
		mcp.WithString("order", mcp.Description("Sort order: asc or desc")),
		withPagination(),
	)
	r.Add("issues", false, searchIssues, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		q, err := req.RequireString("query")
		if err != nil {
			return errResult("search issues", err)
		}
		opts := &github.SearchOptions{
			Sort:        req.GetString("sort", ""),
			Order:       req.GetString("order", ""),
			ListOptions: *listOptions(req),
		}
		result, resp, err := client.Search.Issues(ctx, q, opts)
		if err != nil {
			return errResult("search issues", err)
		}
		return jsonResultWithPage(resp, result)
	})

	updateIssueComment := mcp.NewTool("update_issue_comment",
		mcp.WithDescription("Edit the body of an existing issue or pull request comment."),
		withRepoArgs(),
		mcp.WithNumber("comment_id", mcp.Required(), mcp.Description("Comment ID (from list_issue_comments)")),
		mcp.WithString("body", mcp.Required(), mcp.Description("New comment body (markdown supported)")),
	)
	r.Add("issues", true, updateIssueComment, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		owner, repo, err := ownerRepo(req)
		if err != nil {
			return errResult("update issue comment", err)
		}
		commentID, errRes := int64Arg(req, "comment_id", "update issue comment")
		if errRes != nil {
			return errRes, nil
		}
		body, err := req.RequireString("body")
		if err != nil {
			return errResult("update issue comment", err)
		}
		comment, _, err := client.Issues.EditComment(ctx, owner, repo, commentID, &github.IssueComment{Body: github.Ptr(body)})
		if err != nil {
			return errResult("update issue comment", err)
		}
		return jsonResult(comment)
	})

	deleteIssueComment := mcp.NewTool("delete_issue_comment",
		mcp.WithDescription("Delete an issue or pull request comment by ID."),
		withRepoArgs(),
		mcp.WithNumber("comment_id", mcp.Required(), mcp.Description("Comment ID (from list_issue_comments)")),
	)
	r.Add("issues", true, deleteIssueComment, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		owner, repo, err := ownerRepo(req)
		if err != nil {
			return errResult("delete issue comment", err)
		}
		commentID, errRes := int64Arg(req, "comment_id", "delete issue comment")
		if errRes != nil {
			return errRes, nil
		}
		if _, err := client.Issues.DeleteComment(ctx, owner, repo, commentID); err != nil {
			return errResult("delete issue comment", err)
		}
		return jsonResult(map[string]any{"deleted": true, "comment_id": commentID})
	})

	addSubIssue := mcp.NewTool("add_sub_issue",
		mcp.WithDescription("Attach an existing issue as a sub-issue of a parent issue."),
		withRepoArgs(),
		mcp.WithNumber("issue_number", mcp.Required(), mcp.Description("Parent issue number")),
		mcp.WithNumber("sub_issue_number", mcp.Required(), mcp.Description("Issue number to attach as a sub-issue")),
	)
	r.Add("issues", true, addSubIssue, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		owner, repo, err := ownerRepo(req)
		if err != nil {
			return errResult("add sub-issue", err)
		}
		parent, res := intIDArg(req, "issue_number", "add sub-issue")
		if res != nil {
			return res, nil
		}
		subNumber, res := intIDArg(req, "sub_issue_number", "add sub-issue")
		if res != nil {
			return res, nil
		}
		// The sub-issue API takes the database ID, not the issue number.
		sub, _, err := client.Issues.Get(ctx, owner, repo, subNumber)
		if err != nil {
			return errResult("add sub-issue (resolve sub-issue)", err)
		}
		payload := map[string]any{"sub_issue_id": sub.GetID()}
		path := fmt.Sprintf("repos/%s/issues/%d/sub_issues", repoPath(owner, repo), parent)
		var out any
		if _, err := restCall(ctx, client, "POST", path, payload, &out); err != nil {
			return errResult("add sub-issue", err)
		}
		return jsonResult(out)
	})

	removeSubIssue := mcp.NewTool("remove_sub_issue",
		mcp.WithDescription("Detach a sub-issue from its parent issue."),
		withRepoArgs(),
		mcp.WithNumber("issue_number", mcp.Required(), mcp.Description("Parent issue number")),
		mcp.WithNumber("sub_issue_number", mcp.Required(), mcp.Description("Issue number to detach")),
	)
	r.Add("issues", true, removeSubIssue, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		owner, repo, err := ownerRepo(req)
		if err != nil {
			return errResult("remove sub-issue", err)
		}
		parent, res := intIDArg(req, "issue_number", "remove sub-issue")
		if res != nil {
			return res, nil
		}
		subNumber, res := intIDArg(req, "sub_issue_number", "remove sub-issue")
		if res != nil {
			return res, nil
		}
		sub, _, err := client.Issues.Get(ctx, owner, repo, subNumber)
		if err != nil {
			return errResult("remove sub-issue (resolve sub-issue)", err)
		}
		payload := map[string]any{"sub_issue_id": sub.GetID()}
		path := fmt.Sprintf("repos/%s/issues/%d/sub_issue", repoPath(owner, repo), parent)
		var out any
		if _, err := restCall(ctx, client, "DELETE", path, payload, &out); err != nil {
			return errResult("remove sub-issue", err)
		}
		return jsonResult(map[string]any{"removed": subNumber, "from_issue": parent})
	})

	listIssueTypes := mcp.NewTool("list_issue_types",
		mcp.WithDescription("List the issue types configured for an organization (organization-level feature; "+
			"not available for personal accounts)."),
		mcp.WithString("org", mcp.Required(), mcp.Description("Organization login")),
	)
	r.Add("issues", false, listIssueTypes, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		org, err := req.RequireString("org")
		if err != nil {
			return errResult("list issue types", err)
		}
		types, _, err := client.Organizations.ListIssueTypes(ctx, org)
		if err != nil {
			return errResult("list issue types", err)
		}
		return jsonResult(types)
	})
}
