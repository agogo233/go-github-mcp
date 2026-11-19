package tools

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestListPullRequests(t *testing.T) {
	client, calls := newFakeGitHub(t, func(r *http.Request) (int, string, http.Header) {
		q := r.URL.Query()
		if q.Get("state") != "closed" || q.Get("base") != "main" {
			t.Errorf("list query = %v", q)
		}
		return http.StatusOK, `[{"number":5,"title":"fix bug","state":"closed"}]`, nil
	})
	r := newRecordingRegistrar()
	RegisterPullRequests(r, client)

	text, isErr := callTool(t, r, "list_pull_requests", map[string]any{
		"owner": "o", "repo": "r", "state": "closed", "base": "main",
	})
	if isErr {
		t.Fatalf("unexpected error result: %s", text)
	}
	if !strings.Contains(text, "fix bug") {
		t.Errorf("unexpected payload: %s", text)
	}
	if !strings.HasSuffix((*calls)[0], "GET /api/v3/repos/o/r/pulls") {
		t.Errorf("unexpected calls: %v", *calls)
	}
}

func TestGetPullRequest(t *testing.T) {
	client, calls := newFakeGitHub(t, func(_ *http.Request) (int, string, http.Header) {
		return http.StatusOK, `{"number":5,"title":"fix bug","head":{"ref":"feature","sha":"abc"},"base":{"ref":"main"}}`, nil
	})
	r := newRecordingRegistrar()
	RegisterPullRequests(r, client)

	text, isErr := callTool(t, r, "get_pull_request", map[string]any{"owner": "o", "repo": "r", "pull_number": float64(5)})
	if isErr {
		t.Fatalf("unexpected error result: %s", text)
	}
	if !strings.Contains(text, `"number":5`) || !strings.Contains(text, `"ref":"feature"`) {
		t.Errorf("unexpected payload: %s", text)
	}
	if !strings.HasSuffix((*calls)[0], "GET /api/v3/repos/o/r/pulls/5") {
		t.Errorf("unexpected calls: %v", *calls)
	}
}

func TestCreatePullRequest(t *testing.T) {
	var body string
	client, calls := newFakeGitHub(t, func(r *http.Request) (int, string, http.Header) {
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		return http.StatusCreated, `{"number":6,"title":"feat","state":"open","draft":true}`, nil
	})
	r := newRecordingRegistrar()
	RegisterPullRequests(r, client)

	text, isErr := callTool(t, r, "create_pull_request", map[string]any{
		"owner": "o", "repo": "r", "title": "feat", "head": "feature", "base": "main",
		"body": "desc", "draft": true,
	})
	if isErr {
		t.Fatalf("unexpected error result: %s", text)
	}
	for _, want := range []string{`"title":"feat"`, `"head":"feature"`, `"base":"main"`, `"draft":true`} {
		if !strings.Contains(body, want) {
			t.Errorf("request body %s missing %s", body, want)
		}
	}
	if !strings.Contains(text, `"number":6`) {
		t.Errorf("unexpected payload: %s", text)
	}
	if !strings.HasSuffix((*calls)[0], "POST /api/v3/repos/o/r/pulls") {
		t.Errorf("unexpected calls: %v", *calls)
	}
}

func TestUpdatePullRequest(t *testing.T) {
	var body string
	client, calls := newFakeGitHub(t, func(r *http.Request) (int, string, http.Header) {
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		return http.StatusOK, `{"number":5,"title":"renamed","state":"closed"}`, nil
	})
	r := newRecordingRegistrar()
	RegisterPullRequests(r, client)

	text, isErr := callTool(t, r, "update_pull_request", map[string]any{
		"owner": "o", "repo": "r", "pull_number": float64(5), "title": "renamed", "state": "closed",
	})
	if isErr {
		t.Fatalf("unexpected error result: %s", text)
	}
	if !strings.Contains(body, `"title":"renamed"`) || !strings.Contains(body, `"state":"closed"`) {
		t.Errorf("request body = %s", body)
	}
	if !strings.HasSuffix((*calls)[0], "PATCH /api/v3/repos/o/r/pulls/5") {
		t.Errorf("unexpected calls: %v", *calls)
	}
}

func TestMergePullRequest(t *testing.T) {
	var body string
	client, calls := newFakeGitHub(t, func(r *http.Request) (int, string, http.Header) {
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		return http.StatusOK, `{"merged":true,"sha":"deadbeef","message":"Pull Request successfully merged"}`, nil
	})
	r := newRecordingRegistrar()
	RegisterPullRequests(r, client)

	text, isErr := callTool(t, r, "merge_pull_request", map[string]any{
		"owner": "o", "repo": "r", "pull_number": float64(5),
		"merge_method": "squash", "commit_title": "squash title",
	})
	if isErr {
		t.Fatalf("unexpected error result: %s", text)
	}
	if !strings.Contains(body, `"merge_method":"squash"`) || !strings.Contains(body, `"commit_title":"squash title"`) {
		t.Errorf("request body = %s", body)
	}
	if !strings.Contains(text, `"merged":true`) {
		t.Errorf("unexpected payload: %s", text)
	}
	if !strings.HasSuffix((*calls)[0], "PUT /api/v3/repos/o/r/pulls/5/merge") {
		t.Errorf("unexpected calls: %v", *calls)
	}
}

func TestGetPullRequestFiles(t *testing.T) {
	client, calls := newFakeGitHub(t, func(_ *http.Request) (int, string, http.Header) {
		return http.StatusOK, `[{"filename":"main.go","status":"modified","additions":10,"deletions":2}]`, nil
	})
	r := newRecordingRegistrar()
	RegisterPullRequests(r, client)

	text, isErr := callTool(t, r, "get_pull_request_files", map[string]any{"owner": "o", "repo": "r", "pull_number": float64(5)})
	if isErr {
		t.Fatalf("unexpected error result: %s", text)
	}
	if !strings.Contains(text, "main.go") || !strings.Contains(text, `"additions":10`) {
		t.Errorf("unexpected payload: %s", text)
	}
	if !strings.HasSuffix((*calls)[0], "GET /api/v3/repos/o/r/pulls/5/files") {
		t.Errorf("unexpected calls: %v", *calls)
	}
}

func TestGetPullRequestReviews(t *testing.T) {
	client, calls := newFakeGitHub(t, func(_ *http.Request) (int, string, http.Header) {
		return http.StatusOK, `[{"id":1,"user":{"login":"alice"},"state":"APPROVED","body":"lgtm"}]`, nil
	})
	r := newRecordingRegistrar()
	RegisterPullRequests(r, client)

	text, isErr := callTool(t, r, "get_pull_request_reviews", map[string]any{"owner": "o", "repo": "r", "pull_number": float64(5)})
	if isErr {
		t.Fatalf("unexpected error result: %s", text)
	}
	if !strings.Contains(text, "APPROVED") || !strings.Contains(text, "alice") {
		t.Errorf("unexpected payload: %s", text)
	}
	if !strings.HasSuffix((*calls)[0], "GET /api/v3/repos/o/r/pulls/5/reviews") {
		t.Errorf("unexpected calls: %v", *calls)
	}
}

func TestAddPullRequestCommentGeneral(t *testing.T) {
	var body string
	client, calls := newFakeGitHub(t, func(r *http.Request) (int, string, http.Header) {
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		return http.StatusCreated, `{"id":99,"body":"hello"}`, nil
	})
	r := newRecordingRegistrar()
	RegisterPullRequests(r, client)

	text, isErr := callTool(t, r, "add_pull_request_comment", map[string]any{
		"owner": "o", "repo": "r", "pull_number": float64(5), "body": "hello",
	})
	if isErr {
		t.Fatalf("unexpected error result: %s", text)
	}
	if !strings.Contains(body, `"body":"hello"`) {
		t.Errorf("request body = %s", body)
	}
	// General comments ride on the issues API.
	if !strings.HasSuffix((*calls)[0], "POST /api/v3/repos/o/r/issues/5/comments") {
		t.Errorf("unexpected calls: %v", *calls)
	}
	if !strings.Contains(text, `"id":99`) {
		t.Errorf("unexpected payload: %s", text)
	}
}

func TestAddPullRequestCommentLineAnchored(t *testing.T) {
	var body string
	client, calls := newFakeGitHub(t, func(r *http.Request) (int, string, http.Header) {
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		return http.StatusCreated, `{"id":100,"body":"nit","path":"main.go","line":42}`, nil
	})
	r := newRecordingRegistrar()
	RegisterPullRequests(r, client)

	text, isErr := callTool(t, r, "add_pull_request_comment", map[string]any{
		"owner": "o", "repo": "r", "pull_number": float64(5), "body": "nit",
		"commit_id": "abc123", "path": "main.go", "line": float64(42),
	})
	if isErr {
		t.Fatalf("unexpected error result: %s", text)
	}
	for _, want := range []string{`"commit_id":"abc123"`, `"path":"main.go"`, `"line":42`} {
		if !strings.Contains(body, want) {
			t.Errorf("request body %s missing %s", body, want)
		}
	}
	if !strings.HasSuffix((*calls)[0], "POST /api/v3/repos/o/r/pulls/5/comments") {
		t.Errorf("unexpected calls: %v", *calls)
	}
	if !strings.Contains(text, `"id":100`) {
		t.Errorf("unexpected payload: %s", text)
	}
}

func TestAddPullRequestCommentLineAnchoredRequiresPositiveLine(t *testing.T) {
	client, calls := newFakeGitHub(t, func(_ *http.Request) (int, string, http.Header) {
		return http.StatusCreated, `{}`, nil
	})
	r := newRecordingRegistrar()
	RegisterPullRequests(r, client)

	text, isErr := callTool(t, r, "add_pull_request_comment", map[string]any{
		"owner": "o", "repo": "r", "pull_number": float64(5), "body": "nit",
		"commit_id": "abc123", "path": "main.go",
	})
	if !isErr {
		t.Fatalf("expected error result, got: %s", text)
	}
	if !strings.Contains(text, "line is required") {
		t.Errorf("unexpected error text: %s", text)
	}
	if len(*calls) != 0 {
		t.Errorf("no API call should be made, got: %v", *calls)
	}
}

func TestGetPullRequestStatus(t *testing.T) {
	client, calls := newFakeGitHub(t, func(r *http.Request) (int, string, http.Header) {
		if strings.HasSuffix(r.URL.Path, "/pulls/5") {
			return http.StatusOK, `{"number":5,"head":{"ref":"feature","sha":"abc123"}}`, nil
		}
		return http.StatusOK, `{"state":"success","sha":"abc123","total_count":2,"statuses":[{"state":"success","context":"ci/build"}]}`, nil
	})
	r := newRecordingRegistrar()
	RegisterPullRequests(r, client)

	text, isErr := callTool(t, r, "get_pull_request_status", map[string]any{"owner": "o", "repo": "r", "pull_number": float64(5)})
	if isErr {
		t.Fatalf("unexpected error result: %s", text)
	}
	if !strings.Contains(text, `"state":"success"`) || !strings.Contains(text, "ci/build") {
		t.Errorf("unexpected payload: %s", text)
	}
	if len(*calls) != 2 || !strings.HasSuffix((*calls)[1], "GET /api/v3/repos/o/r/commits/abc123/status") {
		t.Errorf("unexpected calls: %v", *calls)
	}
}

func TestSearchPullRequestsQueryPrefix(t *testing.T) {
	client, calls := newFakeGitHub(t, func(r *http.Request) (int, string, http.Header) {
		if got := r.URL.Query().Get("q"); !strings.HasPrefix(got, "is:pr ") {
			t.Errorf("search q = %q, want is:pr prefix", got)
		}
		return http.StatusOK, `{"total_count":1,"items":[{"number":5,"title":"fix bug","pull_request":{"url":"https://github.com/o/r/pulls/5"}}]}`, nil
	})
	r := newRecordingRegistrar()
	RegisterPullRequests(r, client)

	text, isErr := callTool(t, r, "search_pull_requests", map[string]any{
		"query": "repo:o/r is:open refactor", "sort": "updated", "order": "desc",
	})
	if isErr {
		t.Fatalf("unexpected error result: %s", text)
	}
	if !strings.Contains(text, "fix bug") {
		t.Errorf("unexpected payload: %s", text)
	}
	if len(*calls) != 1 || !strings.Contains((*calls)[0], "GET /api/v3/search/issues") {
		t.Errorf("unexpected calls: %v", *calls)
	}
}
