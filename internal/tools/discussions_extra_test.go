package tools

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestAddDiscussionComment(t *testing.T) {
	var body string
	client, calls := newFakeGitHub(t, func(r *http.Request) (int, string, http.Header) {
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/discussions/4/comments") {
			b, _ := io.ReadAll(r.Body)
			body = string(b)
			return http.StatusCreated, `{"id":99,"body":"welcome"}`, nil
		}
		return 0, "", nil
	})
	r := newRecordingRegistrar()
	RegisterDiscussions(r, client)

	text, isErr := callTool(t, r, "add_discussion_comment", map[string]any{
		"owner": "o", "repo": "r", "discussion_number": float64(4), "body": "welcome",
	})
	if isErr {
		t.Fatalf("unexpected error result: %s", text)
	}
	if !strings.Contains(body, `"body":"welcome"`) {
		t.Errorf("request body = %s", body)
	}
	if !strings.Contains(text, `"id":99`) {
		t.Errorf("unexpected payload: %s", text)
	}
	if len(*calls) != 1 {
		t.Errorf("unexpected calls: %v", *calls)
	}
}

// graphqlBody decodes the JSON body of a GraphQL request captured by the mock.
// graphqlKind reads the JSON body of a GraphQL request exactly once and
// classifies it by the operation it contains.
func graphqlKind(t *testing.T, r *http.Request) (kind, raw string) {
	t.Helper()
	b, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatalf("read graphql body: %v", err)
	}
	raw = string(b)
	if !strings.Contains(raw, `"query":`) {
		t.Fatalf("not a graphql request: %s", raw)
	}
	switch {
	case strings.Contains(raw, "discussionCategories"):
		kind = "discussionCategories"
	case strings.Contains(raw, "createDiscussion"):
		kind = "createDiscussion"
	case strings.Contains(raw, "{ id }"):
		kind = "repositoryId"
	}
	return kind, raw
}

func TestListDiscussionCategoriesViaGraphQL(t *testing.T) {
	var seenKind, raw string
	client, calls := newFakeGitHub(t, func(r *http.Request) (int, string, http.Header) {
		seenKind, raw = graphqlKind(t, r)
		return http.StatusOK, `{"data":{"repository":{"discussionCategories":{"nodes":[
			{"id":"DIC_abc","name":"Ideas","description":"ideas","emoji":"💡","isAnswerable":false}]}}}}`, nil
	})
	r := newRecordingRegistrar()
	RegisterDiscussions(r, client)

	text, isErr := callTool(t, r, "list_discussion_categories", map[string]any{"owner": "o", "repo": "r"})
	if isErr {
		t.Fatalf("unexpected error result: %s", text)
	}
	if seenKind != "discussionCategories" {
		t.Errorf("expected a discussionCategories query, got %q (body: %s)", seenKind, raw)
	}
	if !strings.Contains(text, "DIC_abc") || !strings.Contains(text, "Ideas") {
		t.Errorf("unexpected payload: %s", text)
	}
	if len(*calls) != 1 || (*calls)[0] != "POST /api/v3/graphql" {
		t.Errorf("unexpected calls: %v", *calls)
	}
}

func TestCreateDiscussionViaGraphQL(t *testing.T) {
	var kinds []string
	var createRaw string
	client, calls := newFakeGitHub(t, func(r *http.Request) (int, string, http.Header) {
		kind, raw := graphqlKind(t, r)
		kinds = append(kinds, kind)
		if kind == "createDiscussion" {
			createRaw = raw
			return http.StatusOK, `{"data":{"createDiscussion":{"discussion":{"id":"D_1","number":5,"title":"hello","url":"https://github.com/o/r/discussions/5"}}}}`, nil
		}
		return http.StatusOK, `{"data":{"repository":{"id":"R_1"}}}`, nil
	})
	r := newRecordingRegistrar()
	RegisterDiscussions(r, client)

	text, isErr := callTool(t, r, "create_discussion", map[string]any{
		"owner": "o", "repo": "r", "title": "hello", "body": "world", "category_id": "DIC_abc",
	})
	if isErr {
		t.Fatalf("unexpected error result: %s", text)
	}
	if len(kinds) != 2 || kinds[0] != "repositoryId" || kinds[1] != "createDiscussion" {
		t.Errorf("expected repositoryId lookup then createDiscussion, got %v", kinds)
	}
	if !strings.Contains(createRaw, `"categoryId":"DIC_abc"`) || !strings.Contains(createRaw, `"repoId":"R_1"`) {
		t.Errorf("mutation variables = %s", createRaw)
	}
	if !strings.Contains(text, `"number":5`) {
		t.Errorf("unexpected payload: %s", text)
	}
	if len(*calls) != 2 {
		t.Errorf("unexpected calls: %v", *calls)
	}
}
