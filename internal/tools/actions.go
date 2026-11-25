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
// carry the GitHub token. Redirects are capped well below net/http's default
// of 10 so a hijacked or misconfigured URL cannot be walked far off-host.
var logHTTPClient = &http.Client{
	Timeout: 30 * time.Second,
	CheckRedirect: func(_ *http.Request, via []*http.Request) error {
		if len(via) >= 3 {
			return fmt.Errorf("stopped after %d redirects", len(via))
		}
		return nil
	},
}

// maxLogBytes caps how much log content is read into memory per job.
const maxLogBytes = 5 << 20

// wait_workflow_run tuning: defaults sized for typical CI durations, with a
// hard cap so a bad argument cannot pin a handler goroutine indefinitely.
const (
	waitDefaultTimeoutSeconds  = 600
	waitMaxTimeoutSeconds      = 1800
	waitDefaultIntervalSeconds = 20
	waitMinIntervalSeconds     = 1
	waitMaxConsecutiveFailures = 3
)

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

// maxLogLineBytes caps how many bytes of a single log line are kept before
// the remainder is replaced by a placeholder. CI logs are line-oriented, but
// nothing stops a step from printing megabytes without a newline; the cap
// keeps tailLines memory bounded regardless of the upstream body.
const maxLogLineBytes = 1 << 20

// tailLines keeps the last n lines (<=10000) of r within a bounded memory
// budget. It reports whether scanning stopped early because the log exceeded
// maxLogBytes. Single lines longer than maxLogLineBytes are replaced by a
// placeholder so one pathological line can neither fail the whole fetch nor
// balloon memory; its raw bytes still count toward the overall size cap.
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
	br := bufio.NewReaderSize(r, 64*1024)
	var line []byte
	rawLen := 0
	tooLong := false
	for {
		chunk, err := br.ReadSlice('\n')
		rawLen += len(chunk)
		if !tooLong {
			if len(line)+len(chunk) > maxLogLineBytes {
				line = append(line, chunk[:maxLogLineBytes-len(line)]...)
				tooLong = true
			} else {
				line = append(line, chunk...)
			}
		}
		if err == bufio.ErrBufferFull {
			continue // line longer than the read buffer; keep consuming it
		}
		if rawLen > 0 {
			total += rawLen
			if total > maxLogBytes {
				truncated = true
				break
			}
			if tooLong {
				ring[size%n] = fmt.Sprintf("[line %d omitted: %d bytes exceed the %d-byte line cap]",
					size+1, rawLen, maxLogLineBytes)
			} else {
				ring[size%n] = strings.TrimRight(string(line), "\r\n")
			}
			size++
		}
		if err != nil {
			if err != io.EOF {
				return "", false, err
			}
			break
		}
		line = line[:0]
		rawLen = 0
		tooLong = false
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

	waitWorkflowRun := mcp.NewTool("wait_workflow_run",
		mcp.WithDescription(
			"Block until a GitHub Actions workflow run completes, polling server-side so the caller waits once instead of polling. "+
				"Returns the run JSON once status=completed; if the timeout elapses first, returns the current status with timed_out_waiting=true (call again to keep waiting). "+
				"Already-completed runs return immediately."),
		withRepoArgs(),
		mcp.WithNumber("run_id", mcp.Required(), mcp.Description("Workflow run ID")),
		mcp.WithNumber("timeout_seconds", mcp.Description("Max seconds to block; 0 checks once and returns immediately (default 600, max 1800)")),
		mcp.WithNumber("poll_interval_seconds", mcp.Description("Seconds between GitHub API polls while waiting (default 20, min 1)")),
	)
	r.Add("actions", false, waitWorkflowRun, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		owner, repo, err := ownerRepo(req)
		if err != nil {
			return errResult("wait for workflow run", err)
		}
		runID, errRes := int64Arg(req, "run_id", "wait for workflow run")
		if errRes != nil {
			return errRes, nil
		}
		timeoutSecs := req.GetInt("timeout_seconds", waitDefaultTimeoutSeconds)
		if timeoutSecs < 0 || timeoutSecs > waitMaxTimeoutSeconds {
			return errResult("wait for workflow run", fmt.Errorf("timeout_seconds must be between 0 and %d, got %d", waitMaxTimeoutSeconds, timeoutSecs))
		}
		intervalSecs := req.GetInt("poll_interval_seconds", waitDefaultIntervalSeconds)
		if intervalSecs < waitMinIntervalSeconds || intervalSecs > waitMaxTimeoutSeconds {
			return errResult("wait for workflow run", fmt.Errorf("poll_interval_seconds must be between %d and %d, got %d", waitMinIntervalSeconds, waitMaxTimeoutSeconds, intervalSecs))
		}
		interval := time.Duration(intervalSecs) * time.Second
		deadline := time.Now().Add(time.Duration(timeoutSecs) * time.Second)

		consecutiveFailures := 0
		for {
			run, _, err := client.Actions.GetWorkflowRunByID(ctx, owner, repo, runID)
			if err != nil {
				// Transient API failures (5xx, network blips) must not abort a
				// long wait; only give up after several polls in a row fail.
				consecutiveFailures++
				if consecutiveFailures >= waitMaxConsecutiveFailures {
					return errResult("wait for workflow run", err)
				}
			} else {
				consecutiveFailures = 0
				if run.GetStatus() == "completed" {
					return jsonResult(run)
				}
			}
			remaining := time.Until(deadline)
			if remaining <= 0 {
				if err != nil {
					// The deadline hit while the last poll itself failed:
					// report the API error rather than disguising it as a
					// timeout (e.g. a wrong run_id must not read as
					// "still waiting").
					return errResult("wait for workflow run", err)
				}
				return jsonResult(map[string]any{
					"timed_out_waiting": true,
					"run_id":            runID,
					"status":            run.GetStatus(),
					"conclusion":        run.GetConclusion(),
					"hint":              "run not finished; call wait_workflow_run again to continue waiting, or use get_workflow_run for a one-shot check",
				})
			}
			sleepFor := interval
			if sleepFor > remaining {
				sleepFor = remaining
			}
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(sleepFor):
			}
		}
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

	dispatchWorkflow := mcp.NewTool("dispatch_workflow",
		mcp.WithDescription("Manually trigger a GitHub Actions workflow run (workflow_dispatch event). "+
			"The workflow must declare the workflow_dispatch trigger; ref is the branch or tag to run against."),
		withRepoArgs(),
		mcp.WithString("ref", mcp.Required(), mcp.Description("Git reference to run the workflow against: a branch or tag name")),
		mcp.WithObject("inputs", mcp.Description("Optional workflow_dispatch inputs as a JSON object (max 10 keys); keys must match the workflow's configured inputs")),
		mcp.WithNumber("workflow_id", mcp.Description("Workflow ID (from list_workflows); use this or workflow_file, not both")),
		mcp.WithString("workflow_file", mcp.Description("Workflow file name such as ci.yml or .github/workflows/ci.yml (use this or workflow_id, not both)")),
	)
	r.Add("actions", true, dispatchWorkflow, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		owner, repo, err := ownerRepo(req)
		if err != nil {
			return errResult("dispatch workflow", err)
		}
		ref, err := req.RequireString("ref")
		if err != nil {
			return errResult("dispatch workflow", err)
		}
		workflowID, workflowFile, errRes := workflowTarget(req, "dispatch workflow")
		if errRes != nil {
			return errRes, nil
		}
		var inputs map[string]any
		if raw, ok := req.GetArguments()["inputs"]; ok && !isEmptyArg(raw) {
			obj, isObj := raw.(map[string]any)
			if !isObj {
				res, _ := errResult("dispatch workflow", fmt.Errorf("argument %q must be a JSON object of input keys and values", "inputs"))
				return res, nil
			}
			inputs = obj
		}
		event := github.CreateWorkflowDispatchEventRequest{Ref: ref, Inputs: inputs}
		if workflowFile != "" {
			_, err = client.Actions.CreateWorkflowDispatchEventByFileName(ctx, owner, repo, workflowFile, event)
		} else {
			_, err = client.Actions.CreateWorkflowDispatchEventByID(ctx, owner, repo, workflowID, event)
		}
		if err != nil {
			return errResult("dispatch workflow", err)
		}
		result := map[string]any{"dispatched": true, "ref": ref}
		if workflowFile != "" {
			result["workflow_file"] = workflowFile
		} else {
			result["workflow_id"] = workflowID
		}
		return jsonResult(result)
	})

	enableWorkflow := mcp.NewTool("enable_workflow",
		mcp.WithDescription("Enable a GitHub Actions workflow, setting its state to active so it can run."),
		withRepoArgs(),
		mcp.WithNumber("workflow_id", mcp.Description("Workflow ID (from list_workflows); use this or workflow_file, not both")),
		mcp.WithString("workflow_file", mcp.Description("Workflow file name such as ci.yml or .github/workflows/ci.yml (use this or workflow_id, not both)")),
	)
	r.Add("actions", true, enableWorkflow, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		owner, repo, err := ownerRepo(req)
		if err != nil {
			return errResult("enable workflow", err)
		}
		workflowID, workflowFile, errRes := workflowTarget(req, "enable workflow")
		if errRes != nil {
			return errRes, nil
		}
		if workflowFile != "" {
			_, err = client.Actions.EnableWorkflowByFileName(ctx, owner, repo, workflowFile)
		} else {
			_, err = client.Actions.EnableWorkflowByID(ctx, owner, repo, workflowID)
		}
		if err != nil {
			return errResult("enable workflow", err)
		}
		result := map[string]any{"enabled": true}
		if workflowFile != "" {
			result["workflow_file"] = workflowFile
		} else {
			result["workflow_id"] = workflowID
		}
		return jsonResult(result)
	})

	disableWorkflow := mcp.NewTool("disable_workflow",
		mcp.WithDescription("Disable a GitHub Actions workflow, setting its state to disabled so it cannot run."),
		withRepoArgs(),
		mcp.WithNumber("workflow_id", mcp.Description("Workflow ID (from list_workflows); use this or workflow_file, not both")),
		mcp.WithString("workflow_file", mcp.Description("Workflow file name such as ci.yml or .github/workflows/ci.yml (use this or workflow_id, not both)")),
	)
	r.Add("actions", true, disableWorkflow, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		owner, repo, err := ownerRepo(req)
		if err != nil {
			return errResult("disable workflow", err)
		}
		workflowID, workflowFile, errRes := workflowTarget(req, "disable workflow")
		if errRes != nil {
			return errRes, nil
		}
		if workflowFile != "" {
			_, err = client.Actions.DisableWorkflowByFileName(ctx, owner, repo, workflowFile)
		} else {
			_, err = client.Actions.DisableWorkflowByID(ctx, owner, repo, workflowID)
		}
		if err != nil {
			return errResult("disable workflow", err)
		}
		result := map[string]any{"disabled": true}
		if workflowFile != "" {
			result["workflow_file"] = workflowFile
		} else {
			result["workflow_id"] = workflowID
		}
		return jsonResult(result)
	})

	rerunFailedJobs := mcp.NewTool("rerun_failed_jobs",
		mcp.WithDescription("Re-run only the failed jobs of a GitHub Actions workflow run."),
		withRepoArgs(),
		mcp.WithNumber("run_id", mcp.Required(), mcp.Description("Workflow run ID")),
	)
	r.Add("actions", true, rerunFailedJobs, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		owner, repo, err := ownerRepo(req)
		if err != nil {
			return errResult("rerun failed jobs", err)
		}
		runID, errRes := int64Arg(req, "run_id", "rerun failed jobs")
		if errRes != nil {
			return errRes, nil
		}
		if _, err := client.Actions.RerunFailedJobsByID(ctx, owner, repo, runID); err != nil {
			return errResult("rerun failed jobs", err)
		}
		return jsonResult(map[string]any{"rerun_failed_jobs_requested": runID})
	})
}

// workflowTarget reads the workflow_id / workflow_file argument pair shared by
// the workflow-level write tools. The GitHub API addresses a workflow either
// by its numeric ID or by its file name, so exactly one of the two must be
// provided; anything else is rejected with an actionable message. It returns
// a non-nil error result when the pair is invalid.
func workflowTarget(req mcp.CallToolRequest, action string) (int64, string, *mcp.CallToolResult) {
	idRaw, hasID := req.GetArguments()["workflow_id"]
	fileRaw, hasFile := req.GetArguments()["workflow_file"]
	if hasID && isEmptyArg(idRaw) {
		hasID = false
	}
	if hasFile && isEmptyArg(fileRaw) {
		hasFile = false
	}
	if hasID == hasFile {
		res, _ := errResult(action, fmt.Errorf("provide exactly one of %q or %q", "workflow_id", "workflow_file"))
		return 0, "", res
	}
	if hasID {
		id, ok := int64FromArg(req, "workflow_id")
		if !ok || id <= 0 {
			res, _ := errResult(action, fmt.Errorf("argument %q must be a positive integer workflow ID", "workflow_id"))
			return 0, "", res
		}
		return id, "", nil
	}
	file, err := req.RequireString("workflow_file")
	if err != nil || strings.TrimSpace(file) == "" {
		res, _ := errResult(action, fmt.Errorf("argument %q must be a workflow file name such as %q", "workflow_file", "ci.yml"))
		return 0, "", res
	}
	return 0, strings.TrimSpace(file), nil
}
