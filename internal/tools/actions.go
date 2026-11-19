package tools

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/go-github/v72/github"
	mcp "github.com/mark3labs/mcp-go/mcp"
)

// logHTTPClient fetches pre-signed workflow log URLs. These URLs are served
// from GitHub's log hosts and are pre-authorized, so this client must NOT
// carry the GitHub token.
var logHTTPClient = &http.Client{Timeout: 30 * time.Second}

// maxLogBytes caps how much log content is read into memory per job.
const maxLogBytes = 5 << 20

// fetchLogContent downloads a workflow log from its short-lived URL and
// returns the last tail lines (default: last 10000 lines), plus whether the
// scan hit the size cap before the log ended.
func fetchLogContent(ctx context.Context, url string, tail int) (string, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", false, err
	}
	resp, err := logHTTPClient.Do(req)
	if err != nil {
		return "", false, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", false, fmt.Errorf("log download failed with status %d", resp.StatusCode)
	}
	return tailLines(resp.Body, tail)
}

// tailLines keeps the last n lines (<=10000) of r within a bounded memory
// budget. It reports whether scanning stopped early because the log exceeded
// maxLogBytes.
func tailLines(r io.Reader, n int) (string, bool, error) {
	if n <= 0 {
		// No explicit tail: keep the last 10000 lines, which is also the cap.
		n = 10000
	}
	if n > 10000 {
		n = 10000
	}
	ring := make([]string, n)
	size := 0
	truncated := false
	total := 0
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		total += len(scanner.Bytes()) + 1
		if total > maxLogBytes {
			truncated = true
			break
		}
		ring[size%n] = scanner.Text()
		size++
	}
	if err := scanner.Err(); err != nil {
		return "", false, err
	}
	start := 0
	if size > n {
		start = size - n
	}
	parts := make([]string, 0, size-start)
	for i := start; i < size; i++ {
		parts = append(parts, ring[i%n])
	}
	return strings.Join(parts, "\n"), truncated, nil
}

// RegisterActions registers the "actions" toolset (GitHub Actions CI/CD).
func RegisterActions(r Registrar, client *github.Client) {
	listWorkflows := mcp.NewTool("list_workflows",
		mcp.WithDescription("List GitHub Actions workflows of a repository."),
		withRepoArgs(),
		withPagination(),
	)
	r.Add("actions", false, listWorkflows, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		owner, repo, err := ownerRepo(req)
		if err != nil {
			return errResult("list workflows", err)
		}
		workflows, resp, err := client.Actions.ListWorkflows(ctx, owner, repo, listOptions(req))
		if err != nil {
			return errResult("list workflows", err)
		}
		return jsonResultWithPage(resp, workflows)
	})

	getWorkflow := mcp.NewTool("get_workflow",
		mcp.WithDescription("Get a single GitHub Actions workflow by ID."),
		withRepoArgs(),
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
		mcp.WithDescription("List workflow runs of a repository, optionally scoped to one workflow, branch, status, event, or head commit SHA."),
		withRepoArgs(),
		mcp.WithNumber("workflow_id", mcp.Description("Restrict to this workflow ID (number or numeric string); omit for all workflows")),
		mcp.WithString("branch", mcp.Description("Filter by branch name")),
		mcp.WithString("status", mcp.Description("Filter: queued, in_progress, completed, success, failure, cancelled, ...")),
		mcp.WithString("event", mcp.Description("Filter by event, e.g. push, pull_request, workflow_dispatch")),
		mcp.WithString("head_sha", mcp.Description("Filter by head commit SHA (full 40-char SHA recommended; filters are combined with AND)")),
		withPagination(),
	)
	r.Add("actions", false, listWorkflowRuns, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		owner, repo, err := ownerRepo(req)
		if err != nil {
			return errResult("list workflow runs", err)
		}
		opts := &github.ListWorkflowRunsOptions{
			Branch:      req.GetString("branch", ""),
			Status:      req.GetString("status", ""),
			Event:       req.GetString("event", ""),
			HeadSHA:     req.GetString("head_sha", ""),
			ListOptions: *listOptions(req),
		}
		var runs *github.WorkflowRuns
		var resp *github.Response
		// An absent or empty workflow_id means "all workflows"; a present but
		// invalid value must fail loudly instead of silently listing all runs.
		if raw, exists := req.GetArguments()["workflow_id"]; exists && !isEmptyArg(raw) {
			wid, ok := int64FromArg(req, "workflow_id")
			if !ok || wid <= 0 {
				return errResult("list workflow runs", fmt.Errorf("argument %q must be a positive integer workflow ID, got %v", "workflow_id", raw))
			}
			runs, resp, err = client.Actions.ListWorkflowRunsByID(ctx, owner, repo, wid, opts)
		} else {
			runs, resp, err = client.Actions.ListRepositoryWorkflowRuns(ctx, owner, repo, opts)
		}
		if err != nil {
			return errResult("list workflow runs", err)
		}
		return jsonResultWithPage(resp, runs)
	})

	getWorkflowRun := mcp.NewTool("get_workflow_run",
		mcp.WithDescription("Get a single GitHub Actions workflow run."),
		withRepoArgs(),
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
		withRepoArgs(),
		mcp.WithNumber("run_id", mcp.Required(), mcp.Description("Workflow run ID")),
		withPagination(),
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
		jobs, resp, err := client.Actions.ListWorkflowJobs(ctx, owner, repo, runID, &github.ListWorkflowJobsOptions{
			ListOptions: *listOptions(req),
		})
		if err != nil {
			return errResult("list workflow jobs", err)
		}
		return jsonResultWithPage(resp, jobs)
	})

	getWorkflowRunLogs := mcp.NewTool("get_workflow_run_logs",
		mcp.WithDescription("Get a short-lived download URL for the logs of a workflow run, or the log content itself with return_content."),
		withRepoArgs(),
		mcp.WithNumber("run_id", mcp.Required(), mcp.Description("Workflow run ID")),
		mcp.WithBoolean("return_content", mcp.Description("Fetch the log content inline instead of returning a download URL")),
		mcp.WithNumber("tail_lines", mcp.Description("With return_content, keep only the last N lines (default: all lines, capped at 10000)")),
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
		if !req.GetBool("return_content", false) {
			return jsonResult(map[string]any{
				"run_id":     runID,
				"logs_url":   logsURL.String(),
				"expires_at": "short-lived; download soon",
			})
		}
		content, truncated, err := fetchLogContent(ctx, logsURL.String(), req.GetInt("tail_lines", 0))
		if err != nil {
			return errResult("get workflow run logs (content)", err)
		}
		entry := map[string]any{
			"run_id":    runID,
			"content":   content,
			"truncated": truncated,
		}
		if truncated {
			entry["note"] = "log truncated: exceeded the in-memory size cap"
		}
		return jsonResult(entry)
	})

	rerunWorkflow := mcp.NewTool("rerun_workflow",
		mcp.WithDescription("Re-run a completed GitHub Actions workflow run."),
		withRepoArgs(),
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
		withRepoArgs(),
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

	getJobLogs := mcp.NewTool("get_job_logs",
		mcp.WithDescription("Get logs for a workflow job. Provide exactly one of job_id, or run_id with failed_only=true "+
			"to collect the logs of all failed jobs in a run. return_content=true downloads the log text "+
			"(tail_lines keeps only the last lines); otherwise a short-lived URL is returned."),
		withRepoArgs(),
		mcp.WithNumber("job_id", mcp.Description("Workflow job ID (use this or run_id, not both)")),
		mcp.WithNumber("run_id", mcp.Description("Workflow run ID; with failed_only=true collects failed jobs' logs")),
		mcp.WithBoolean("failed_only", mcp.Description("With run_id: only collect jobs whose conclusion is failure (default true; set false to collect every job)")),
		mcp.WithBoolean("return_content", mcp.Description("Return the actual log text instead of a download URL")),
		mcp.WithNumber("tail_lines", mcp.Description("With return_content: keep only the last N lines (default 10000, max 10000)")),
	)
	r.Add("actions", false, getJobLogs, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		owner, repo, err := ownerRepo(req)
		if err != nil {
			return errResult("get job logs", err)
		}
		jobRaw, hasJob := req.GetArguments()["job_id"]
		runRaw, hasRun := req.GetArguments()["run_id"]
		if hasJob && isEmptyArg(jobRaw) {
			hasJob = false
		}
		if hasRun && isEmptyArg(runRaw) {
			hasRun = false
		}
		var jobID, runID int64
		if hasJob {
			jid, ok := int64FromArg(req, "job_id")
			if !ok || jid <= 0 {
				return errResult("get job logs", fmt.Errorf("argument %q must be a positive integer job ID", "job_id"))
			}
			jobID = jid
		}
		if hasRun {
			rid, ok := int64FromArg(req, "run_id")
			if !ok || rid <= 0 {
				return errResult("get job logs", fmt.Errorf("argument %q must be a positive integer run ID", "run_id"))
			}
			runID = rid
		}
		if hasJob == hasRun {
			return errResult("get job logs", fmt.Errorf("provide exactly one of %q or %q (the latter with failed_only=true)", "job_id", "run_id"))
		}
		returnContent := req.GetBool("return_content", false)
		tail := req.GetInt("tail_lines", 0)

		collect := func(jobID int64, name, conclusion string) (map[string]any, error) {
			logsURL, _, err := client.Actions.GetWorkflowJobLogs(ctx, owner, repo, jobID, 3)
			if err != nil {
				return nil, err
			}
			entry := map[string]any{"job_id": jobID}
			if name != "" {
				entry["name"] = name
			}
			if conclusion != "" {
				entry["conclusion"] = conclusion
			}
			if !returnContent {
				entry["logs_url"] = logsURL.String()
				entry["expires_at"] = "short-lived; download soon"
				return entry, nil
			}
			content, truncated, err := fetchLogContent(ctx, logsURL.String(), tail)
			if err != nil {
				return nil, err
			}
			entry["content"] = content
			if truncated {
				entry["note"] = "log truncated: exceeded the in-memory size cap"
			}
			return entry, nil
		}

		if hasJob {
			entry, err := collect(jobID, "", "")
			if err != nil {
				return errResult("get job logs", err)
			}
			return jsonResult(entry)
		}

		// run_id mode: scan the run's jobs (first page of 100); by default only
		// failed jobs are collected, failed_only=false collects them all.
		failedOnly := req.GetBool("failed_only", true)
		jobs, _, err := client.Actions.ListWorkflowJobs(ctx, owner, repo, runID, &github.ListWorkflowJobsOptions{
			ListOptions: github.ListOptions{PerPage: 100},
		})
		if err != nil {
			return errResult("get job logs", err)
		}
		result := map[string]any{"run_id": runID}
		collected := make([]map[string]any, 0)
		for _, j := range jobs.Jobs {
			if failedOnly && j.GetConclusion() != "failure" {
				continue
			}
			entry, err := collect(j.GetID(), j.GetName(), j.GetConclusion())
			if err != nil {
				return errResult("get job logs", err)
			}
			collected = append(collected, entry)
		}
		result["jobs"] = collected
		if jobs.GetTotalCount() > len(jobs.Jobs) {
			result["note"] = fmt.Sprintf("scanned only the first %d jobs of this run", len(jobs.Jobs))
		} else if len(collected) == 0 && failedOnly {
			result["note"] = "no failed jobs found"
		}
		return jsonResult(result)
	})

	listWorkflowRunArtifacts := mcp.NewTool("list_workflow_run_artifacts",
		mcp.WithDescription("List artifacts of a workflow run."),
		withRepoArgs(),
		mcp.WithNumber("run_id", mcp.Required(), mcp.Description("Workflow run ID")),
		withPagination(),
	)
	r.Add("actions", false, listWorkflowRunArtifacts, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		owner, repo, err := ownerRepo(req)
		if err != nil {
			return errResult("list workflow run artifacts", err)
		}
		runID, errRes := int64Arg(req, "run_id", "list workflow run artifacts")
		if errRes != nil {
			return errRes, nil
		}
		artifacts, resp, err := client.Actions.ListWorkflowRunArtifacts(ctx, owner, repo, runID, listOptions(req))
		if err != nil {
			return errResult("list workflow run artifacts", err)
		}
		return jsonResultWithPage(resp, artifacts)
	})

	getWorkflowRunArtifact := mcp.NewTool("get_workflow_run_artifact",
		mcp.WithDescription("Get metadata and a short-lived download URL for a workflow run artifact (a zip archive)."),
		withRepoArgs(),
		mcp.WithNumber("artifact_id", mcp.Required(), mcp.Description("Artifact ID (from list_workflow_run_artifacts)")),
	)
	r.Add("actions", false, getWorkflowRunArtifact, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		owner, repo, err := ownerRepo(req)
		if err != nil {
			return errResult("get workflow run artifact", err)
		}
		artifactID, errRes := int64Arg(req, "artifact_id", "get workflow run artifact")
		if errRes != nil {
			return errRes, nil
		}
		artifact, _, err := client.Actions.GetArtifact(ctx, owner, repo, artifactID)
		if err != nil {
			return errResult("get workflow run artifact", err)
		}
		downloadURL, _, err := client.Actions.DownloadArtifact(ctx, owner, repo, artifactID, 3)
		if err != nil {
			return errResult("get workflow run artifact", err)
		}
		return jsonResult(map[string]any{
			"id":            artifact.GetID(),
			"name":          artifact.GetName(),
			"size_in_bytes": artifact.GetSizeInBytes(),
			"expired":       artifact.GetExpired(),
			"download_url":  downloadURL.String(),
			"note":          "the archive is a zip; the URL is short-lived",
		})
	})
}
