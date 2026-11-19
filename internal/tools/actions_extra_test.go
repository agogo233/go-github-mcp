package tools

import (
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
