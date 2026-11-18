package tools

import (
	"context"

	"github.com/google/go-github/v72/github"
	mcp "github.com/mark3labs/mcp-go/mcp"
)

// int64Arg reads a required int64 argument (workflow/run/job IDs).
func int64Arg(req mcp.CallToolRequest, key, action string) (int64, *mcp.CallToolResult) {
	v, err := req.RequireInt(key)
	if err != nil {
		res, _ := errResult(action, err)
		return 0, res
	}
	return int64(v), nil
}

// RegisterActions registers the "actions" toolset (GitHub Actions CI/CD).
func RegisterActions(r Registrar, client *github.Client) {
	listWorkflows := mcp.NewTool("list_workflows",
		mcp.WithDescription("List GitHub Actions workflows of a repository."),
		mcp.WithString("owner", mcp.Required(), mcp.Description("Repository owner")),
		mcp.WithString("repo", mcp.Required(), mcp.Description("Repository name")),
		mcp.WithNumber("page", mcp.Description("Page number (starts at 1)")),
		mcp.WithNumber("per_page", mcp.Description("Results per page (default 30, max 100)")),
	)
	r.Add("actions", false, listWorkflows, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		owner, repo, err := ownerRepo(req)
		if err != nil {
			return errResult("list workflows", err)
		}
		workflows, _, err := client.Actions.ListWorkflows(ctx, owner, repo, listOptions(req))
		if err != nil {
			return errResult("list workflows", err)
		}
		return jsonResult(workflows)
	})

	getWorkflow := mcp.NewTool("get_workflow",
		mcp.WithDescription("Get a single GitHub Actions workflow by ID."),
		mcp.WithString("owner", mcp.Required(), mcp.Description("Repository owner")),
		mcp.WithString("repo", mcp.Required(), mcp.Description("Repository name")),
		mcp.WithNumber("workflow_id", mcp.Required(), mcp.Description("Workflow ID")),
	)
	r.Add("actions", false, getWorkflow, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		owner, repo, err := ownerRepo(req)
		if err != nil {
			return errResult("get workflow", err)
		}
		workflowID, errRes := int64Arg(req, "workflow_id", "get workflow")
		if errRes != nil {
			return errRes, nil
		}
		workflow, _, err := client.Actions.GetWorkflowByID(ctx, owner, repo, workflowID)
		if err != nil {
			return errResult("get workflow", err)
		}
		return jsonResult(workflow)
	})

	listWorkflowRuns := mcp.NewTool("list_workflow_runs",
		mcp.WithDescription("List workflow runs of a repository, optionally scoped to one workflow."),
		mcp.WithString("owner", mcp.Required(), mcp.Description("Repository owner")),
		mcp.WithString("repo", mcp.Required(), mcp.Description("Repository name")),
		mcp.WithNumber("workflow_id", mcp.Description("Restrict to this workflow ID; omit for all workflows")),
		mcp.WithString("branch", mcp.Description("Filter by branch name")),
		mcp.WithString("status", mcp.Description("Filter: queued, in_progress, completed, success, failure, cancelled, ...")),
		mcp.WithString("event", mcp.Description("Filter by event, e.g. push, pull_request, workflow_dispatch")),
		mcp.WithNumber("page", mcp.Description("Page number (starts at 1)")),
		mcp.WithNumber("per_page", mcp.Description("Results per page (default 30, max 100)")),
	)
	r.Add("actions", false, listWorkflowRuns, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		owner, repo, err := ownerRepo(req)
		if err != nil {
			return errResult("list workflow runs", err)
		}
		opts := &github.ListWorkflowRunsOptions{
			Branch: req.GetString("branch", ""),
			Status: req.GetString("status", ""),
			Event:  req.GetString("event", ""),
			ListOptions: func() github.ListOptions {
				page, perPage := pagination(req)
				return github.ListOptions{Page: page, PerPage: perPage}
			}(),
		}
		var runs *github.WorkflowRuns
		if wid := req.GetInt("workflow_id", 0); wid > 0 {
			runs, _, err = client.Actions.ListWorkflowRunsByID(ctx, owner, repo, int64(wid), opts)
		} else {
			runs, _, err = client.Actions.ListRepositoryWorkflowRuns(ctx, owner, repo, opts)
		}
		if err != nil {
			return errResult("list workflow runs", err)
		}
		return jsonResult(runs)
	})

	getWorkflowRun := mcp.NewTool("get_workflow_run",
		mcp.WithDescription("Get a single GitHub Actions workflow run."),
		mcp.WithString("owner", mcp.Required(), mcp.Description("Repository owner")),
		mcp.WithString("repo", mcp.Required(), mcp.Description("Repository name")),
		mcp.WithNumber("run_id", mcp.Required(), mcp.Description("Workflow run ID")),
	)
	r.Add("actions", false, getWorkflowRun, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		owner, repo, err := ownerRepo(req)
		if err != nil {
			return errResult("get workflow run", err)
		}
		runID, errRes := int64Arg(req, "run_id", "get workflow run")
		if errRes != nil {
			return errRes, nil
		}
		run, _, err := client.Actions.GetWorkflowRunByID(ctx, owner, repo, runID)
		if err != nil {
			return errResult("get workflow run", err)
		}
		return jsonResult(run)
	})

	listWorkflowJobs := mcp.NewTool("list_workflow_jobs",
		mcp.WithDescription("List jobs of a workflow run, with step-level status."),
		mcp.WithString("owner", mcp.Required(), mcp.Description("Repository owner")),
		mcp.WithString("repo", mcp.Required(), mcp.Description("Repository name")),
		mcp.WithNumber("run_id", mcp.Required(), mcp.Description("Workflow run ID")),
		mcp.WithNumber("page", mcp.Description("Page number (starts at 1)")),
		mcp.WithNumber("per_page", mcp.Description("Results per page (default 30, max 100)")),
	)
	r.Add("actions", false, listWorkflowJobs, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		owner, repo, err := ownerRepo(req)
		if err != nil {
			return errResult("list workflow jobs", err)
		}
		runID, errRes := int64Arg(req, "run_id", "list workflow jobs")
		if errRes != nil {
			return errRes, nil
		}
		jobs, _, err := client.Actions.ListWorkflowJobs(ctx, owner, repo, runID, &github.ListWorkflowJobsOptions{
			ListOptions: *listOptions(req),
		})
		if err != nil {
			return errResult("list workflow jobs", err)
		}
		return jsonResult(jobs)
	})

	getWorkflowRunLogs := mcp.NewTool("get_workflow_run_logs",
		mcp.WithDescription("Get a short-lived download URL for the logs of a workflow run."),
		mcp.WithString("owner", mcp.Required(), mcp.Description("Repository owner")),
		mcp.WithString("repo", mcp.Required(), mcp.Description("Repository name")),
		mcp.WithNumber("run_id", mcp.Required(), mcp.Description("Workflow run ID")),
	)
	r.Add("actions", false, getWorkflowRunLogs, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		owner, repo, err := ownerRepo(req)
		if err != nil {
			return errResult("get workflow run logs", err)
		}
		runID, errRes := int64Arg(req, "run_id", "get workflow run logs")
		if errRes != nil {
			return errRes, nil
		}
		logsURL, _, err := client.Actions.GetWorkflowRunLogs(ctx, owner, repo, runID, 3)
		if err != nil {
			return errResult("get workflow run logs", err)
		}
		return jsonResult(map[string]any{
			"logs_url":   logsURL.String(),
			"expires_at": "short-lived; download soon",
		})
	})

	rerunWorkflow := mcp.NewTool("rerun_workflow",
		mcp.WithDescription("Re-run a completed GitHub Actions workflow run."),
		mcp.WithString("owner", mcp.Required(), mcp.Description("Repository owner")),
		mcp.WithString("repo", mcp.Required(), mcp.Description("Repository name")),
		mcp.WithNumber("run_id", mcp.Required(), mcp.Description("Workflow run ID")),
	)
	r.Add("actions", true, rerunWorkflow, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		owner, repo, err := ownerRepo(req)
		if err != nil {
			return errResult("rerun workflow", err)
		}
		runID, errRes := int64Arg(req, "run_id", "rerun workflow")
		if errRes != nil {
			return errRes, nil
		}
		if _, err := client.Actions.RerunWorkflowByID(ctx, owner, repo, runID); err != nil {
			return errResult("rerun workflow", err)
		}
		return jsonResult(map[string]any{"rerun_requested": runID})
	})

	cancelWorkflowRun := mcp.NewTool("cancel_workflow_run",
		mcp.WithDescription("Cancel an in-progress GitHub Actions workflow run."),
		mcp.WithString("owner", mcp.Required(), mcp.Description("Repository owner")),
		mcp.WithString("repo", mcp.Required(), mcp.Description("Repository name")),
		mcp.WithNumber("run_id", mcp.Required(), mcp.Description("Workflow run ID")),
	)
	r.Add("actions", true, cancelWorkflowRun, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		owner, repo, err := ownerRepo(req)
		if err != nil {
			return errResult("cancel workflow run", err)
		}
		runID, errRes := int64Arg(req, "run_id", "cancel workflow run")
		if errRes != nil {
			return errRes, nil
		}
		if _, err := client.Actions.CancelWorkflowRunByID(ctx, owner, repo, runID); err != nil {
			return errResult("cancel workflow run", err)
		}
		return jsonResult(map[string]any{"cancelled": runID})
	})
}
