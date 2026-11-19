package tools

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestSearchIssuesPassesQueryThrough(t *testing.T) {
	var gotQuery string
	client, _ := newFakeGitHub(t, func(r *http.Request) (int, string, http.Header) {
		gotQuery = r.URL.Query().Get("q")
		return http.StatusOK, `{"total_count":1,"items":[{"number":3,"title":"bug"}]}`, nil
	})
	r := newRecordingRegistrar()
	RegisterIssues(r, client)

	text, isErr := callTool(t, r, "search_issues", map[string]any{"query": "repo:o/r is:open bug"})
	if isErr {
		t.Fatalf("unexpected error result: %s", text)
	}
	if gotQuery != "repo:o/r is:open bug" {
		t.Errorf("query = %q, want it passed through unchanged", gotQuery)
	}
	if !strings.Contains(text, `"title":"bug"`) {
		t.Errorf("unexpected payload: %s", text)
	}
}

func TestSearchPullRequestsPrependsIsPR(t *testing.T) {
	var gotQuery string
	client, _ := newFakeGitHub(t, func(r *http.Request) (int, string, http.Header) {
		gotQuery = r.URL.Query().Get("q")
		return http.StatusOK, `{"total_count":1,"items":[{"number":7,"title":"refactor"}]}`, nil
	})
	r := newRecordingRegistrar()
	RegisterPullRequests(r, client)

	text, isErr := callTool(t, r, "search_pull_requests", map[string]any{"query": "repo:o/r refactor"})
	if isErr {
		t.Fatalf("unexpected error result: %s", text)
	}
	if gotQuery != "is:pr repo:o/r refactor" {
		t.Errorf("query = %q, want 'is:pr ' prefix added", gotQuery)
	}
	if !strings.Contains(text, `"title":"refactor"`) {
		t.Errorf("unexpected payload: %s", text)
	}
}

func TestUpdateIssueComment(t *testing.T) {
	client, calls := newFakeGitHub(t, func(_ *http.Request) (int, string, http.Header) {
		return http.StatusOK, `{"id":123,"body":"edited body"}`, nil
	})
	r := newRecordingRegistrar()
	RegisterIssues(r, client)

	text, isErr := callTool(t, r, "update_issue_comment", map[string]any{
		"owner": "o", "repo": "r", "comment_id": float64(123), "body": "edited body",
	})
	if isErr {
		t.Fatalf("unexpected error result: %s", text)
	}
	if !strings.Contains(text, "edited body") {
		t.Errorf("unexpected payload: %s", text)
	}
	if len(*calls) != 1 || (*calls)[0] != "PATCH /api/v3/repos/o/r/issues/comments/123" {
		t.Errorf("unexpected calls: %v", *calls)
	}
}

func TestAddSubIssueResolvesDatabaseID(t *testing.T) {
	var addBody string
	client, calls := newFakeGitHub(t, func(r *http.Request) (int, string, http.Header) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/issues/2"):
			return http.StatusOK, `{"id":555,"number":2,"title":"sub"}`, nil
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/issues/1/sub_issues"):
			b, _ := io.ReadAll(r.Body)
			addBody = string(b)
			return http.StatusCreated, `[{"id":555,"number":2,"title":"sub"}]`, nil
		}
		return 0, "", nil
	})
	r := newRecordingRegistrar()
	RegisterIssues(r, client)

	text, isErr := callTool(t, r, "add_sub_issue", map[string]any{
		"owner": "o", "repo": "r", "issue_number": float64(1), "sub_issue_number": float64(2),
	})
	if isErr {
		t.Fatalf("unexpected error result: %s", text)
	}
	if !strings.Contains(addBody, `"sub_issue_id":555`) {
		t.Errorf("add must send the database ID, body: %s", addBody)
	}
	if !strings.Contains(text, `"number":2`) {
		t.Errorf("unexpected payload: %s", text)
	}
	all := strings.Join(*calls, "\n")
	if !strings.Contains(all, "GET /api/v3/repos/o/r/issues/2") ||
		!strings.Contains(all, "POST /api/v3/repos/o/r/issues/1/sub_issues") {
		t.Errorf("unexpected calls:\n%s", all)
	}
}

func TestRemoveSubIssue(t *testing.T) {
	var delBody string
	client, calls := newFakeGitHub(t, func(r *http.Request) (int, string, http.Header) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/issues/2"):
			return http.StatusOK, `{"id":555,"number":2,"title":"sub"}`, nil
		case r.Method == http.MethodDelete && strings.HasSuffix(r.URL.Path, "/issues/1/sub_issue"):
			b, _ := io.ReadAll(r.Body)
			delBody = string(b)
			return http.StatusNoContent, ``, nil
		}
		return 0, "", nil
	})
	r := newRecordingRegistrar()
	RegisterIssues(r, client)

	text, isErr := callTool(t, r, "remove_sub_issue", map[string]any{
		"owner": "o", "repo": "r", "issue_number": float64(1), "sub_issue_number": float64(2),
	})
	if isErr {
		t.Fatalf("unexpected error result: %s", text)
	}
	if !strings.Contains(delBody, `"sub_issue_id":555`) {
		t.Errorf("remove must send the database ID, body: %s", delBody)
	}
	if !strings.Contains(text, `"removed":2`) {
		t.Errorf("unexpected payload: %s", text)
	}
	all := strings.Join(*calls, "\n")
	if !strings.Contains(all, "DELETE /api/v3/repos/o/r/issues/1/sub_issue") {
		t.Errorf("unexpected calls:\n%s", all)
	}
}

func TestListIssueTypes(t *testing.T) {
	client, calls := newFakeGitHub(t, func(_ *http.Request) (int, string, http.Header) {
		return http.StatusOK, `[{"id":1,"name":"Bug","enabled":true}]`, nil
	})
	r := newRecordingRegistrar()
	RegisterIssues(r, client)

	text, isErr := callTool(t, r, "list_issue_types", map[string]any{"org": "acme"})
	if isErr {
		t.Fatalf("unexpected error result: %s", text)
	}
	if !strings.Contains(text, "Bug") {
		t.Errorf("unexpected payload: %s", text)
	}
	if len(*calls) != 1 || (*calls)[0] != "GET /api/v3/orgs/acme/issue-types" {
		t.Errorf("unexpected calls: %v", *calls)
	}
}
