package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/google/go-github/v72/github"
	mcp "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
)

// recordingRegistrar captures registered tools so tests can invoke handlers
// directly without the MCP server layer.
type recordingRegistrar struct {
	names    []string
	handlers map[string]mcpserver.ToolHandlerFunc
}

func newRecordingRegistrar() *recordingRegistrar {
	return &recordingRegistrar{handlers: map[string]mcpserver.ToolHandlerFunc{}}
}

func (r *recordingRegistrar) Add(_ string, _ bool, tool mcp.Tool, handler mcpserver.ToolHandlerFunc) {
	r.names = append(r.names, tool.GetName())
	r.handlers[tool.GetName()] = handler
}

// newFakeGitHub starts an httptest server standing in for the GitHub API and
// returns a client wired to it plus the list of recorded "METHOD /path" calls.
// respond receives every request; a zero status falls back to 404 Not Found.
func newFakeGitHub(t *testing.T, respond func(r *http.Request) (int, string, http.Header)) (*github.Client, *[]string) {
	t.Helper()
	var mu sync.Mutex
	var calls []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls = append(calls, r.Method+" "+r.URL.Path)
		mu.Unlock()
		status, body, header := respond(r)
		if status == 0 {
			status = http.StatusNotFound
			body = `{"message":"Not Found"}`
		}
		for k, vs := range header {
			for _, v := range vs {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(ts.Close)
	client, err := github.NewClient(nil).WithEnterpriseURLs(ts.URL+"/api/v3/", ts.URL+"/api/uploads/")
	if err != nil {
		t.Fatalf("invalid fake GitHub base URL: %v", err)
	}
	return client, &calls
}

// callTool invokes a registered handler with the given arguments and returns
// the raw text content of the first content block.
func callTool(t *testing.T, r *recordingRegistrar, name string, args map[string]any) (string, bool) {
	t.Helper()
	handler, ok := r.handlers[name]
	if !ok {
		t.Fatalf("tool %q was not registered", name)
	}
	res, err := handler(context.Background(), reqWithArgs(args))
	if err != nil {
		t.Fatalf("handler returned protocol error: %v", err)
	}
	if len(res.Content) == 0 {
		t.Fatalf("result has no content blocks")
	}
	text := res.Content[0].(mcp.TextContent).Text
	return text, res.IsError
}

func TestListIssuesEmptyResultIsArrayNotNull(t *testing.T) {
	client, _ := newFakeGitHub(t, func(_ *http.Request) (int, string, http.Header) {
		return http.StatusOK, `null`, nil
	})
	r := newRecordingRegistrar()
	RegisterIssues(r, client)

	text, isErr := callTool(t, r, "list_issues", map[string]any{"owner": "o", "repo": "r"})
	if isErr {
		t.Fatalf("unexpected error result: %s", text)
	}
	if text != "[]" {
		t.Errorf("empty list serialized as %q, want []", text)
	}
}

func TestListIssuesSuccessKeepsArrayShape(t *testing.T) {
	client, _ := newFakeGitHub(t, func(_ *http.Request) (int, string, http.Header) {
		return http.StatusOK, `[{"number":1,"title":"bug"}]`, nil
	})
	r := newRecordingRegistrar()
	RegisterIssues(r, client)

	text, isErr := callTool(t, r, "list_issues", map[string]any{"owner": "o", "repo": "r"})
	if isErr {
		t.Fatalf("unexpected error result: %s", text)
	}
	var issues []map[string]any
	if err := json.Unmarshal([]byte(text), &issues); err != nil {
		t.Fatalf("content[0] is not a JSON array: %v (%s)", err, text)
	}
	if len(issues) != 1 || issues[0]["title"] != "bug" {
		t.Errorf("unexpected payload: %s", text)
	}
}

func TestErrorResultsCarryActionableHints(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		body       string
		header     http.Header
		tool       string
		args       map[string]any
		wantSubstr []string
	}{
		{
			name:       "not found suggests missing access",
			status:     http.StatusNotFound,
			body:       `{"message":"Not Found"}`,
			tool:       "get_issue",
			args:       map[string]any{"owner": "o", "repo": "r", "issue_number": float64(7)},
			wantSubstr: []string{"404", "lacks access"},
		},
		{
			name:       "validation failure lists field errors",
			status:     http.StatusUnprocessableEntity,
			body:       `{"message":"Validation Failed","errors":[{"resource":"Issue","field":"title","code":"missing_field"}]}`,
			tool:       "get_issue",
			args:       map[string]any{"owner": "o", "repo": "r", "issue_number": float64(7)},
			wantSubstr: []string{"validation failed", "title", "missing_field"},
		},
		{
			name:       "rate limit points at reset time",
			status:     http.StatusForbidden,
			body:       `{"message":"API rate limit exceeded"}`,
			header:     http.Header{"X-Ratelimit-Remaining": []string{"0"}, "X-Ratelimit-Limit": []string{"60"}},
			tool:       "get_issue",
			args:       map[string]any{"owner": "o", "repo": "r", "issue_number": float64(7)},
			wantSubstr: []string{"rate limit"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, _ := newFakeGitHub(t, func(_ *http.Request) (int, string, http.Header) {
				return tt.status, tt.body, tt.header
			})
			r := newRecordingRegistrar()
			RegisterIssues(r, client)

			text, isErr := callTool(t, r, tt.tool, tt.args)
			if !isErr {
				t.Fatalf("expected error result, got: %s", text)
			}
			for _, want := range tt.wantSubstr {
				if !strings.Contains(text, want) {
					t.Errorf("error message %q does not contain %q", text, want)
				}
			}
		})
	}
}

func TestPushFilesSuccess(t *testing.T) {
	client, calls := newFakeGitHub(t, func(r *http.Request) (int, string, http.Header) {
		switch {
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/v3/repos/o/r/git/ref/"):
			return http.StatusOK, `{"ref":"refs/heads/main","object":{"sha":"basesha","type":"commit"}}`, nil
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/v3/repos/o/r/commits/"):
			return http.StatusOK, `{"sha":"basesha","commit":{"tree":{"sha":"basetree"}}}`, nil
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/git/blobs"):
			return http.StatusOK, `{"sha":"blobsha"}`, nil
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/git/trees"):
			return http.StatusOK, `{"sha":"newtree"}`, nil
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/git/commits"):
			return http.StatusOK, `{"sha":"newcommit"}`, nil
		case r.Method == http.MethodPatch && strings.Contains(r.URL.Path, "/git/refs"):
			return http.StatusOK, `{"ref":"refs/heads/main","object":{"sha":"newcommit","type":"commit"}}`, nil
		}
		return 0, "", nil
	})
	r := newRecordingRegistrar()
	RegisterGit(r, client)

	text, isErr := callTool(t, r, "push_files", map[string]any{
		"owner": "o", "repo": "r", "branch": "main", "message": "add files",
		"files": []any{map[string]any{"path": "a.txt", "content": "hello"}},
	})
	if isErr {
		t.Fatalf("unexpected error result: %s", text)
	}
	if !strings.Contains(text, "newcommit") {
		t.Errorf("result should carry the new commit SHA, got: %s", text)
	}
	all := strings.Join(*calls, "\n")
	for _, want := range []string{"/git/blobs", "/git/trees", "/git/commits", "/git/refs"} {
		if !strings.Contains(all, want) {
			t.Errorf("expected the commit orchestration to call %s, calls were:\n%s", want, all)
		}
	}
}

func TestPushFilesMissingBranchReportsRemedy(t *testing.T) {
	client, _ := newFakeGitHub(t, func(_ *http.Request) (int, string, http.Header) {
		return http.StatusNotFound, `{"message":"Not Found"}`, nil
	})
	r := newRecordingRegistrar()
	RegisterGit(r, client)

	text, isErr := callTool(t, r, "push_files", map[string]any{
		"owner": "o", "repo": "r", "branch": "nope", "message": "m",
		"files": []any{map[string]any{"path": "a.txt", "content": "hi"}},
	})
	if !isErr {
		t.Fatalf("expected error result, got: %s", text)
	}
	if !strings.Contains(text, "create it first with create_branch") {
		t.Errorf("error should point at the remedy, got: %s", text)
	}
}

func TestListIssuesAppendsPaginationMetadata(t *testing.T) {
	link := http.Header{"Link": []string{`</api/v3/repos/o/r/issues?page=2&per_page=30>; rel="next"`}}
	client, _ := newFakeGitHub(t, func(_ *http.Request) (int, string, http.Header) {
		return http.StatusOK, `[{"number":1,"title":"bug"}]`, link
	})
	r := newRecordingRegistrar()
	RegisterIssues(r, client)

	res, err := r.handlers["list_issues"](context.Background(), reqWithArgs(map[string]any{"owner": "o", "repo": "r"}))
	if err != nil {
		t.Fatalf("handler returned protocol error: %v", err)
	}
	if len(res.Content) != 2 {
		t.Fatalf("want 2 content blocks (payload + pagination), got %d", len(res.Content))
	}
	if first := res.Content[0].(mcp.TextContent).Text; !strings.HasPrefix(first, "[") {
		t.Errorf("first block must stay the JSON array, got %q", first)
	}
	if second := res.Content[1].(mcp.TextContent).Text; !strings.Contains(second, `"has_next":true`) || !strings.Contains(second, `"next_page":2`) {
		t.Errorf("pagination block = %q", second)
	}
}

func TestListIssuesPaginationMarksLastPage(t *testing.T) {
	client, _ := newFakeGitHub(t, func(_ *http.Request) (int, string, http.Header) {
		return http.StatusOK, `[]`, nil
	})
	r := newRecordingRegistrar()
	RegisterIssues(r, client)

	res, err := r.handlers["list_issues"](context.Background(), reqWithArgs(map[string]any{"owner": "o", "repo": "r"}))
	if err != nil {
		t.Fatalf("handler returned protocol error: %v", err)
	}
	if len(res.Content) != 2 {
		t.Fatalf("want 2 content blocks (payload + pagination), got %d", len(res.Content))
	}
	if second := res.Content[1].(mcp.TextContent).Text; !strings.Contains(second, `"has_next":false`) {
		t.Errorf("pagination block = %q, want has_next false", second)
	}
}

func TestListWorkflowRunsInvalidWorkflowIDFails(t *testing.T) {
	client, calls := newFakeGitHub(t, func(_ *http.Request) (int, string, http.Header) {
		return http.StatusOK, `{"total_count":0,"workflow_runs":[]}`, nil
	})
	r := newRecordingRegistrar()
	RegisterActions(r, client)

	for _, bad := range []any{"abc", float64(-1), float64(0), true} {
		text, isErr := callTool(t, r, "list_workflow_runs", map[string]any{"owner": "o", "repo": "r", "workflow_id": bad})
		if !isErr {
			t.Errorf("workflow_id %v: expected error result, got: %s", bad, text)
		}
	}
	if len(*calls) != 0 {
		t.Errorf("invalid workflow_id must not reach the API, calls: %v", *calls)
	}
}

func TestListWorkflowRunsEmptyWorkflowIDListsAll(t *testing.T) {
	client, calls := newFakeGitHub(t, func(_ *http.Request) (int, string, http.Header) {
		return http.StatusOK, `{"total_count":0,"workflow_runs":[]}`, nil
	})
	r := newRecordingRegistrar()
	RegisterActions(r, client)

	text, isErr := callTool(t, r, "list_workflow_runs", map[string]any{"owner": "o", "repo": "r", "workflow_id": ""})
	if isErr {
		t.Fatalf("empty workflow_id must list all runs, got error: %s", text)
	}
	if len(*calls) == 0 {
		t.Error("expected the all-runs API call to be made")
	}
}

func TestListWorkflowRunsNumericStringWorkflowID(t *testing.T) {
	client, calls := newFakeGitHub(t, func(_ *http.Request) (int, string, http.Header) {
		return http.StatusOK, `{"total_count":0,"workflow_runs":[]}`, nil
	})
	r := newRecordingRegistrar()
	RegisterActions(r, client)

	text, isErr := callTool(t, r, "list_workflow_runs", map[string]any{"owner": "o", "repo": "r", "workflow_id": "123"})
	if isErr {
		t.Fatalf("numeric-string workflow_id must be accepted, got error: %s", text)
	}
	if len(*calls) != 1 || !strings.Contains((*calls)[0], "/actions/workflows/123/runs") {
		t.Errorf("expected the per-workflow API call, calls: %v", *calls)
	}
}
