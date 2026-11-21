package tools

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTailLinesKeepsLastN(t *testing.T) {
	got, truncated, err := tailLines(strings.NewReader("a\nb\nc\nd\n"), 2)
	if err != nil || truncated {
		t.Fatalf("err=%v truncated=%v", err, truncated)
	}
	if got != "c\nd" {
		t.Errorf("tailLines = %q, want %q", got, "c\nd")
	}
}

func TestTailLinesAllWhenFewerThanN(t *testing.T) {
	got, truncated, err := tailLines(strings.NewReader("a\nb\n"), 10)
	if err != nil || truncated {
		t.Fatalf("err=%v truncated=%v", err, truncated)
	}
	if got != "a\nb" {
		t.Errorf("tailLines = %q, want %q", got, "a\nb")
	}
}

func TestTailLinesZeroKeepsAllLines(t *testing.T) {
	got, truncated, err := tailLines(strings.NewReader("a\nb\nc\n"), 0)
	if err != nil || truncated {
		t.Fatalf("err=%v truncated=%v", err, truncated)
	}
	if got != "a\nb\nc" {
		t.Errorf("tailLines(0) = %q, want all lines %q", got, "a\nb\nc")
	}
}

func TestTailLinesMarksOversizedLogTruncated(t *testing.T) {
	big := strings.Repeat("line\n", 1_200_000) // ~6MB, above the 5MB cap
	got, truncated, err := tailLines(strings.NewReader(big), 5)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !truncated {
		t.Error("oversized log must be marked truncated")
	}
	if got == "" {
		t.Error("tail content must survive truncation")
	}
}

// A single line beyond the per-line cap must be replaced by a placeholder
// instead of failing the whole fetch or ballooning memory.
func TestTailLinesReplacesOverlongLineWithPlaceholder(t *testing.T) {
	long := strings.Repeat("x", 2<<20) // 2 MiB single line
	input := "before\n" + long + "\nafter\n"
	got, truncated, err := tailLines(strings.NewReader(input), 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if truncated {
		t.Error("2 MiB total is below the log cap; truncated must stay false")
	}
	if !strings.Contains(got, "before") || !strings.Contains(got, "after") {
		t.Errorf("neighbouring lines must survive, got: %q", got)
	}
	if strings.Contains(got, "xxxxx") {
		t.Error("overlong line content must not be kept")
	}
	if !strings.Contains(got, "omitted") {
		t.Errorf("overlong line must be replaced by a placeholder, got: %q", got)
	}
}

// Raw bytes of an overlong line count toward the overall size cap.
func TestTailLinesOverlongLineCountsTowardCap(t *testing.T) {
	long := strings.Repeat("y", 6<<20) // 6 MiB single line, above the 5MB cap
	got, truncated, err := tailLines(strings.NewReader(long+"\nend\n"), 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !truncated {
		t.Error("6 MiB line must trip the size cap")
	}
	if strings.Contains(got, "yyyyy") {
		t.Error("overlong line content must not be kept")
	}
}

// A final line without a trailing newline is still kept, matching the
// previous Scanner-based behavior.
func TestTailLinesKeepsFinalLineWithoutNewline(t *testing.T) {
	got, truncated, err := tailLines(strings.NewReader("a\nb\nc"), 0)
	if err != nil || truncated {
		t.Fatalf("err=%v truncated=%v", err, truncated)
	}
	if got != "a\nb\nc" {
		t.Errorf("tailLines = %q, want %q", got, "a\nb\nc")
	}
}

func TestTailLinesTrimsCarriageReturns(t *testing.T) {
	got, truncated, err := tailLines(strings.NewReader("a\r\nb\r\n"), 0)
	if err != nil || truncated {
		t.Fatalf("err=%v truncated=%v", err, truncated)
	}
	if got != "a\nb" {
		t.Errorf("tailLines = %q, want %q", got, "a\nb")
	}
}

// startLogServer returns the URL of a standalone server serving a fixed log
// body; the fake GitHub API redirects log requests to it.
func startLogServer(t *testing.T, body string) *string {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(ts.Close)
	url := ts.URL + "/job.log"
	return &url
}

func TestGetJobLogsURLMode(t *testing.T) {
	logURL := startLogServer(t, "hello log")
	client, _ := newFakeGitHub(t, func(_ *http.Request) (int, string, http.Header) {
		return http.StatusFound, ``, http.Header{"Location": []string{*logURL}}
	})
	r := newRecordingRegistrar()
	RegisterActions(r, client)

	text, isErr := callTool(t, r, "get_job_logs", map[string]any{
		"owner": "o", "repo": "r", "job_id": float64(11),
	})
	if isErr {
		t.Fatalf("unexpected error result: %s", text)
	}
	if !strings.Contains(text, "logs_url") {
		t.Errorf("URL mode must return logs_url, got: %s", text)
	}
}

func TestGetJobLogsContentModeWithTail(t *testing.T) {
	logURL := startLogServer(t, "l1\nl2\nl3\nl4\n")
	client, _ := newFakeGitHub(t, func(_ *http.Request) (int, string, http.Header) {
		return http.StatusFound, ``, http.Header{"Location": []string{*logURL}}
	})
	r := newRecordingRegistrar()
	RegisterActions(r, client)

	text, isErr := callTool(t, r, "get_job_logs", map[string]any{
		"owner": "o", "repo": "r", "job_id": float64(11),
		"return_content": true, "tail_lines": float64(2),
	})
	if isErr {
		t.Fatalf("unexpected error result: %s", text)
	}
	if !strings.Contains(text, `"content":"l3\nl4"`) {
		t.Errorf("content must be the last two lines, got: %s", text)
	}
}

func TestGetJobLogsRequiresExactlyOneSelector(t *testing.T) {
	client, _ := newFakeGitHub(t, func(_ *http.Request) (int, string, http.Header) {
		return http.StatusOK, `{}`, nil
	})
	r := newRecordingRegistrar()
	RegisterActions(r, client)

	if _, isErr := callTool(t, r, "get_job_logs", map[string]any{"owner": "o", "repo": "r"}); !isErr {
		t.Error("neither job_id nor run_id must fail")
	}
	if _, isErr := callTool(t, r, "get_job_logs", map[string]any{
		"owner": "o", "repo": "r", "job_id": float64(1), "run_id": float64(2),
	}); !isErr {
		t.Error("both job_id and run_id must fail")
	}
	if _, isErr := callTool(t, r, "get_job_logs", map[string]any{
		"owner": "o", "repo": "r", "job_id": "abc",
	}); !isErr {
		t.Error("invalid job_id must fail")
	}
}

func TestGetJobLogsFailedOnlyCollectsFailures(t *testing.T) {
	logURL := startLogServer(t, "boom\nat step 3\n")
	client, _ := newFakeGitHub(t, func(r *http.Request) (int, string, http.Header) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/actions/runs/9/jobs"):
			return http.StatusOK, `{"total_count":2,"jobs":[
				{"id":11,"name":"good","conclusion":"success"},
				{"id":22,"name":"bad","conclusion":"failure"}]}`, nil
		case strings.HasSuffix(r.URL.Path, "/actions/jobs/22/logs"):
			return http.StatusFound, ``, http.Header{"Location": []string{*logURL}}
		}
		return 0, "", nil
	})
	r := newRecordingRegistrar()
	RegisterActions(r, client)

	text, isErr := callTool(t, r, "get_job_logs", map[string]any{
		"owner": "o", "repo": "r", "run_id": float64(9),
		"failed_only": true, "return_content": true,
	})
	if isErr {
		t.Fatalf("unexpected error result: %s", text)
	}
	if !strings.Contains(text, `"name":"bad"`) || !strings.Contains(text, "at step 3") {
		t.Errorf("failed job log must be collected, got: %s", text)
	}
	if strings.Contains(text, `"name":"good"`) {
		t.Errorf("successful jobs must be skipped, got: %s", text)
	}
}

func TestGetJobLogsFailedOnlyFalseCollectsEveryJob(t *testing.T) {
	logURL := startLogServer(t, "log")
	client, _ := newFakeGitHub(t, func(r *http.Request) (int, string, http.Header) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/actions/runs/9/jobs"):
			return http.StatusOK, `{"total_count":2,"jobs":[
				{"id":11,"name":"good","conclusion":"success"},
				{"id":22,"name":"bad","conclusion":"failure"}]}`, nil
		case strings.HasSuffix(r.URL.Path, "/actions/jobs/11/logs"),
			strings.HasSuffix(r.URL.Path, "/actions/jobs/22/logs"):
			return http.StatusFound, ``, http.Header{"Location": []string{*logURL}}
		}
		return 0, "", nil
	})
	r := newRecordingRegistrar()
	RegisterActions(r, client)

	text, isErr := callTool(t, r, "get_job_logs", map[string]any{
		"owner": "o", "repo": "r", "run_id": float64(9),
		"failed_only": false, "return_content": true,
	})
	if isErr {
		t.Fatalf("unexpected error result: %s", text)
	}
	if !strings.Contains(text, `"name":"good"`) || !strings.Contains(text, `"name":"bad"`) {
		t.Errorf("failed_only=false must collect every job, got: %s", text)
	}
}

func TestWorkflowRunArtifacts(t *testing.T) {
	logURL := startLogServer(t, "")
	client, calls := newFakeGitHub(t, func(r *http.Request) (int, string, http.Header) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/actions/runs/9/artifacts"):
			return http.StatusOK, `{"total_count":1,"artifacts":[{"id":77,"name":"dist","size_in_bytes":2048,"expired":false}]}`, nil
		case strings.HasSuffix(r.URL.Path, "/actions/artifacts/77"):
			return http.StatusOK, `{"id":77,"name":"dist","size_in_bytes":2048,"expired":false}`, nil
		case strings.HasSuffix(r.URL.Path, "/actions/artifacts/77/zip"):
			return http.StatusFound, ``, http.Header{"Location": []string{*logURL}}
		}
		return 0, "", nil
	})
	r := newRecordingRegistrar()
	RegisterActions(r, client)

	text, isErr := callTool(t, r, "list_workflow_run_artifacts", map[string]any{
		"owner": "o", "repo": "r", "run_id": float64(9),
	})
	if isErr || !strings.Contains(text, "dist") {
		t.Errorf("list_workflow_run_artifacts: isErr=%v text=%s", isErr, text)
	}

	text, isErr = callTool(t, r, "get_workflow_run_artifact", map[string]any{
		"owner": "o", "repo": "r", "artifact_id": float64(77),
	})
	if isErr {
		t.Fatalf("get_workflow_run_artifact: unexpected error result: %s", text)
	}
	if !strings.Contains(text, `"name":"dist"`) || !strings.Contains(text, "download_url") {
		t.Errorf("unexpected payload: %s", text)
	}
	all := strings.Join(*calls, "\n")
	if !strings.Contains(all, "/actions/artifacts/77/zip") {
		t.Errorf("expected the artifact download redirect call, calls:\n%s", all)
	}
}

func TestGetWorkflowRunLogsURLMode(t *testing.T) {
	logURL := startLogServer(t, "log")
	client, calls := newFakeGitHub(t, func(r *http.Request) (int, string, http.Header) {
		if strings.HasSuffix(r.URL.Path, "/actions/runs/9/logs") {
			return http.StatusFound, ``, http.Header{"Location": []string{*logURL}}
		}
		return 0, "", nil
	})
	r := newRecordingRegistrar()
	RegisterActions(r, client)

	text, isErr := callTool(t, r, "get_workflow_run_logs", map[string]any{"owner": "o", "repo": "r", "run_id": float64(9)})
	if isErr {
		t.Fatalf("unexpected error result: %s", text)
	}
	if !strings.Contains(text, "logs_url") || strings.Contains(text, `"content"`) {
		t.Errorf("default mode must return a download URL only, got: %s", text)
	}
	if len(*calls) != 1 || !strings.HasSuffix((*calls)[0], "GET /api/v3/repos/o/r/actions/runs/9/logs") {
		t.Errorf("unexpected calls: %v", *calls)
	}
}

func TestGetWorkflowRunLogsContentModeWithTail(t *testing.T) {
	logURL := startLogServer(t, "a\nb\nc\n")
	client, calls := newFakeGitHub(t, func(r *http.Request) (int, string, http.Header) {
		if strings.HasSuffix(r.URL.Path, "/actions/runs/9/logs") {
			return http.StatusFound, ``, http.Header{"Location": []string{*logURL}}
		}
		return 0, "", nil
	})
	r := newRecordingRegistrar()
	RegisterActions(r, client)

	text, isErr := callTool(t, r, "get_workflow_run_logs", map[string]any{
		"owner": "o", "repo": "r", "run_id": float64(9),
		"return_content": true, "tail_lines": float64(2),
	})
	if isErr {
		t.Fatalf("unexpected error result: %s", text)
	}
	if !strings.Contains(text, `"content":"b\nc"`) || !strings.Contains(text, `"truncated":false`) {
		t.Errorf("unexpected payload: %s", text)
	}
	if len(*calls) != 1 {
		t.Errorf("unexpected calls: %v", *calls)
	}
}

// head_sha must be forwarded to the GitHub API as the head_sha query
// parameter so callers can scope runs to a specific commit.
func TestListWorkflowRunsFiltersByHeadSHA(t *testing.T) {
	var gotHeadSHA string
	client, _ := newFakeGitHub(t, func(r *http.Request) (int, string, http.Header) {
		gotHeadSHA = r.URL.Query().Get("head_sha")
		return http.StatusOK, `{"total_count":0,"workflow_runs":[]}`, nil
	})
	r := newRecordingRegistrar()
	RegisterActions(r, client)

	sha := "0123456789abcdef0123456789abcdef01234567"
	text, isErr := callTool(t, r, "list_workflow_runs", map[string]any{
		"owner": "o", "repo": "r", "head_sha": sha,
	})
	if isErr {
		t.Fatalf("unexpected error result: %s", text)
	}
	if gotHeadSHA != sha {
		t.Errorf("head_sha not forwarded: got %q, want %q", gotHeadSHA, sha)
	}
}

// An absent head_sha must not add the query parameter at all, keeping the
// request identical to the pre-filter behavior.
func TestListWorkflowRunsOmitsHeadSHAByDefault(t *testing.T) {
	var rawQuery string
	client, _ := newFakeGitHub(t, func(r *http.Request) (int, string, http.Header) {
		rawQuery = r.URL.RawQuery
		return http.StatusOK, `{"total_count":0,"workflow_runs":[]}`, nil
	})
	r := newRecordingRegistrar()
	RegisterActions(r, client)

	text, isErr := callTool(t, r, "list_workflow_runs", map[string]any{
		"owner": "o", "repo": "r",
	})
	if isErr {
		t.Fatalf("unexpected error result: %s", text)
	}
	if strings.Contains(rawQuery, "head_sha") {
		t.Errorf("head_sha must not be sent when omitted, query: %s", rawQuery)
	}
}

// Redirect chains are capped: a looping (or hijacked) log URL must fail fast
// instead of following net/http's default ten hops.
func TestFetchLogContentRejectsRedirectLoops(t *testing.T) {
	var calls int
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		http.Redirect(w, r, "/next", http.StatusFound)
	}))
	defer ts.Close()

	_, _, err := fetchLogContent(context.Background(), ts.URL, 10)
	if err == nil {
		t.Fatal("expected the redirect loop to be rejected")
	}
	if calls > 4 {
		t.Errorf("followed %d requests, want at most initial + 3 redirects", calls)
	}
}
