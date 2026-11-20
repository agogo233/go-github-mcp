package tools

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

// The GraphQL list tools must surface pagination metadata so callers can
// tell that the hard first-page cap (50) is not the whole dataset.
func TestListProjectsSurfacesPageInfo(t *testing.T) {
	var query string
	client, _ := newFakeGitHub(t, func(r *http.Request) (int, string, http.Header) {
		body, _ := io.ReadAll(r.Body)
		query = string(body)
		return http.StatusOK,
			`{"data":{"repository":{"projectsV2":{"pageInfo":{"hasNextPage":true},` +
				`"nodes":[{"id":"1","title":"Board","number":1,"url":"u","closed":false}]}}}}`,
			nil
	})
	r := newRecordingRegistrar()
	RegisterProjects(r, client)

	text, isErr := callTool(t, r, "list_projects", map[string]any{"owner": "o", "repo": "r"})
	if isErr {
		t.Fatalf("unexpected error result: %s", text)
	}
	if !strings.Contains(query, "pageInfo") {
		t.Errorf("GraphQL query should request pageInfo, got: %s", query)
	}
	if !strings.Contains(text, "hasNextPage") {
		t.Errorf("result should carry pageInfo.hasNextPage, got: %s", text)
	}
}

func TestListDiscussionCategoriesSurfacesPageInfo(t *testing.T) {
	var query string
	client, _ := newFakeGitHub(t, func(r *http.Request) (int, string, http.Header) {
		body, _ := io.ReadAll(r.Body)
		query = string(body)
		return http.StatusOK,
			`{"data":{"repository":{"discussionCategories":{"pageInfo":{"hasNextPage":false},` +
				`"nodes":[{"id":"c1","name":"General"}]}}}}`,
			nil
	})
	r := newRecordingRegistrar()
	RegisterDiscussions(r, client)

	text, isErr := callTool(t, r, "list_discussion_categories", map[string]any{"owner": "o", "repo": "r"})
	if isErr {
		t.Fatalf("unexpected error result: %s", text)
	}
	if !strings.Contains(query, "pageInfo") {
		t.Errorf("GraphQL query should request pageInfo, got: %s", query)
	}
	if !strings.Contains(text, "hasNextPage") {
		t.Errorf("result should carry pageInfo.hasNextPage, got: %s", text)
	}
}
