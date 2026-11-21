package tools

import (
	"net/http"
	"strings"
	"testing"
)

// GraphQL failures arrive as HTTP 200 with a non-empty "errors" array; the
// tools must report them as failed tool calls, not as successful results.
func TestGraphQLToolsReportGraphQLErrors(t *testing.T) {
	client, _ := newFakeGitHub(t, func(_ *http.Request) (int, string, http.Header) {
		return http.StatusOK, `{"errors":[{"message":"Could not resolve to a node with the global id 'DIC_bad'"}]}`, nil
	})
	r := newRecordingRegistrar()
	RegisterProjects(r, client)
	RegisterDiscussions(r, client)

	tests := []struct {
		tool string
		args map[string]any
	}{
		{"list_projects", map[string]any{"owner": "o", "repo": "r"}},
		{"list_discussion_categories", map[string]any{"owner": "o", "repo": "r"}},
		{"create_discussion", map[string]any{"owner": "o", "repo": "r", "title": "t", "body": "b", "category_id": "DIC_bad"}},
	}
	for _, tt := range tests {
		t.Run(tt.tool, func(t *testing.T) {
			text, isErr := callTool(t, r, tt.tool, tt.args)
			if !isErr {
				t.Fatalf("%s must fail on GraphQL errors, got success: %s", tt.tool, text)
			}
			if !strings.Contains(text, "Could not resolve to a node") {
				t.Errorf("%s error should carry the GraphQL message, got: %s", tt.tool, text)
			}
		})
	}
}

// Partial errors — a non-empty errors array returned alongside data — must
// still fail the tool call rather than look like a success.
func TestGraphQLPartialErrorsFailTheToolCall(t *testing.T) {
	client, _ := newFakeGitHub(t, func(_ *http.Request) (int, string, http.Header) {
		return http.StatusOK, `{"data":{"repository":null},"errors":[{"message":"node not found"}]}`, nil
	})
	r := newRecordingRegistrar()
	RegisterProjects(r, client)

	text, isErr := callTool(t, r, "list_projects", map[string]any{"owner": "o", "repo": "r"})
	if !isErr {
		t.Fatalf("partial GraphQL errors must fail the tool call, got success: %s", text)
	}
	if !strings.Contains(text, "node not found") {
		t.Errorf("error should carry the GraphQL message, got: %s", text)
	}
}

// A GraphQL error on the createDiscussion mutation itself (after the
// repository lookup succeeded) must surface as a failed tool call.
func TestCreateDiscussionMutationErrorsSurface(t *testing.T) {
	client, _ := newFakeGitHub(t, func(r *http.Request) (int, string, http.Header) {
		kind, _ := graphqlKind(t, r)
		if kind == "repositoryId" {
			return http.StatusOK, `{"data":{"repository":{"id":"R_1"}}}`, nil
		}
		return http.StatusOK, `{"errors":[{"message":"category not found"}]}`, nil
	})
	r := newRecordingRegistrar()
	RegisterDiscussions(r, client)

	text, isErr := callTool(t, r, "create_discussion", map[string]any{
		"owner": "o", "repo": "r", "title": "t", "body": "b", "category_id": "DIC_bad",
	})
	if !isErr {
		t.Fatalf("mutation GraphQL errors must fail the tool call, got success: %s", text)
	}
	if !strings.Contains(text, "category not found") {
		t.Errorf("error should carry the GraphQL message, got: %s", text)
	}
}

// Successful responses must keep decoding the data payload for callers.
func TestGraphQLSuccessStillDecodesData(t *testing.T) {
	client, _ := newFakeGitHub(t, func(_ *http.Request) (int, string, http.Header) {
		return http.StatusOK, `{"data":{"repository":{"projectsV2":{"pageInfo":{"hasNextPage":false},` +
			`"nodes":[{"id":"1","title":"Board","number":1,"url":"u","closed":false}]}}}}`, nil
	})
	r := newRecordingRegistrar()
	RegisterProjects(r, client)

	text, isErr := callTool(t, r, "list_projects", map[string]any{"owner": "o", "repo": "r"})
	if isErr {
		t.Fatalf("unexpected error result: %s", text)
	}
	if !strings.Contains(text, `"title":"Board"`) {
		t.Errorf("data payload missing, got: %s", text)
	}
}
