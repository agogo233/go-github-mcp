package tools

import (
	"context"

	"github.com/google/go-github/v72/github"
	mcp "github.com/mark3labs/mcp-go/mcp"
)

// RegisterLabels registers the "labels" toolset.
func RegisterLabels(r Registrar, client *github.Client) {
	listLabels := mcp.NewTool("list_labels",
		mcp.WithDescription("List all labels defined in a repository."),
		withRepoArgs(),
		withPagination(),
	)
	r.Add("labels", false, listLabels, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		owner, repo, err := ownerRepo(req)
		if err != nil {
			return errResult("list labels", err)
		}
		labels, _, err := client.Issues.ListLabels(ctx, owner, repo, listOptions(req))
		if err != nil {
			return errResult("list labels", err)
		}
		return jsonResult(labels)
	})

	getLabel := mcp.NewTool("get_label",
		mcp.WithDescription("Get a single label by name."),
		withRepoArgs(),
		mcp.WithString("name", mcp.Required(), mcp.Description("Label name")),
	)
	r.Add("labels", false, getLabel, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		owner, repo, err := ownerRepo(req)
		if err != nil {
			return errResult("get label", err)
		}
		name, err := req.RequireString("name")
		if err != nil {
			return errResult("get label", err)
		}
		label, _, err := client.Issues.GetLabel(ctx, owner, repo, name)
		if err != nil {
			return errResult("get label", err)
		}
		return jsonResult(label)
	})

	createLabel := mcp.NewTool("create_label",
		mcp.WithDescription("Create a new repository label."),
		withRepoArgs(),
		mcp.WithString("name", mcp.Required(), mcp.Description("Label name")),
		mcp.WithString("color", mcp.Description("Hex color without '#', e.g. 'D73A4A' (default:ededed)")),
		mcp.WithString("description", mcp.Description("Short label description")),
	)
	r.Add("labels", true, createLabel, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		owner, repo, err := ownerRepo(req)
		if err != nil {
			return errResult("create label", err)
		}
		name, err := req.RequireString("name")
		if err != nil {
			return errResult("create label", err)
		}
		label := &github.Label{Name: github.Ptr(name)}
		if v := req.GetString("color", ""); v != "" {
			label.Color = github.Ptr(v)
		}
		if v := req.GetString("description", ""); v != "" {
			label.Description = github.Ptr(v)
		}
		created, _, err := client.Issues.CreateLabel(ctx, owner, repo, label)
		if err != nil {
			return errResult("create label", err)
		}
		return jsonResult(created)
	})

	updateLabel := mcp.NewTool("update_label",
		mcp.WithDescription("Update an existing label's name, color or description."),
		withRepoArgs(),
		mcp.WithString("name", mcp.Required(), mcp.Description("Current label name")),
		mcp.WithString("new_name", mcp.Description("New label name")),
		mcp.WithString("color", mcp.Description("New hex color without '#'")),
		mcp.WithString("description", mcp.Description("New description")),
	)
	r.Add("labels", true, updateLabel, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		owner, repo, err := ownerRepo(req)
		if err != nil {
			return errResult("update label", err)
		}
		name, err := req.RequireString("name")
		if err != nil {
			return errResult("update label", err)
		}
		label := &github.Label{}
		if v := req.GetString("new_name", ""); v != "" {
			label.Name = github.Ptr(v)
		}
		if v := req.GetString("color", ""); v != "" {
			label.Color = github.Ptr(v)
		}
		if v := req.GetString("description", ""); v != "" {
			label.Description = github.Ptr(v)
		}
		updated, _, err := client.Issues.EditLabel(ctx, owner, repo, name, label)
		if err != nil {
			return errResult("update label", err)
		}
		return jsonResult(updated)
	})

	deleteLabel := mcp.NewTool("delete_label",
		mcp.WithDescription("Delete a repository label (it disappears from all issues)."),
		withRepoArgs(),
		mcp.WithString("name", mcp.Required(), mcp.Description("Label name to delete")),
	)
	r.Add("labels", true, deleteLabel, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		owner, repo, err := ownerRepo(req)
		if err != nil {
			return errResult("delete label", err)
		}
		name, err := req.RequireString("name")
		if err != nil {
			return errResult("delete label", err)
		}
		if _, err := client.Issues.DeleteLabel(ctx, owner, repo, name); err != nil {
			return errResult("delete label", err)
		}
		return jsonResult(map[string]any{"deleted": name})
	})

	addIssueLabels := mcp.NewTool("add_issue_labels",
		mcp.WithDescription("Add one or more labels to an issue."),
		withRepoArgs(),
		mcp.WithNumber("issue_number", mcp.Required(), mcp.Description("Issue number")),
		mcp.WithArray("labels", mcp.Required(),
			mcp.Description("Label names to add"),
			mcp.Items(map[string]any{"type": "string"}),
		),
	)
	r.Add("labels", true, addIssueLabels, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		owner, repo, err := ownerRepo(req)
		if err != nil {
			return errResult("add issue labels", err)
		}
		number, err := req.RequireInt("issue_number")
		if err != nil {
			return errResult("add issue labels", err)
		}
		labels := req.GetStringSlice("labels", nil)
		if len(labels) == 0 {
			return mcp.NewToolResultError("labels must contain at least one name"), nil
		}
		added, _, err := client.Issues.AddLabelsToIssue(ctx, owner, repo, number, labels)
		if err != nil {
			return errResult("add issue labels", err)
		}
		return jsonResult(added)
	})

	removeIssueLabel := mcp.NewTool("remove_issue_label",
		mcp.WithDescription("Remove a single label from an issue."),
		withRepoArgs(),
		mcp.WithNumber("issue_number", mcp.Required(), mcp.Description("Issue number")),
		mcp.WithString("label", mcp.Required(), mcp.Description("Label name to remove")),
	)
	r.Add("labels", true, removeIssueLabel, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		owner, repo, err := ownerRepo(req)
		if err != nil {
			return errResult("remove issue label", err)
		}
		number, err := req.RequireInt("issue_number")
		if err != nil {
			return errResult("remove issue label", err)
		}
		label, err := req.RequireString("label")
		if err != nil {
			return errResult("remove issue label", err)
		}
		if _, err := client.Issues.RemoveLabelForIssue(ctx, owner, repo, number, label); err != nil {
			return errResult("remove issue label", err)
		}
		return jsonResult(map[string]any{"removed": label, "issue_number": number})
	})
}
