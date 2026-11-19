package tools

import (
	"net/http"
	"strings"
	"testing"
)

func TestSearchCodeSuccess(t *testing.T) {
	client, calls := newFakeGitHub(t, func(_ *http.Request) (int, string, http.Header) {
		return http.StatusOK, `{"total_count":1,"items":[{"name":"main.go","path":"main.go","sha":"abc","repository":{"full_name":"o/r"}}]}`, nil
	})
	r := newRecordingRegistrar()
	RegisterRepos(r, client)

	text, isErr := callTool(t, r, "search_code", map[string]any{"query": "repo:o/r todo"})
	if isErr {
		t.Fatalf("unexpected error result: %s", text)
	}
	if !strings.Contains(text, `"total_count":1`) || !strings.Contains(text, "main.go") {
		t.Errorf("unexpected payload: %s", text)
	}
	if len(*calls) != 1 || (*calls)[0] != "GET /api/v3/search/code" {
		t.Errorf("unexpected calls: %v", *calls)
	}
}

func TestSearchCommitsSuccess(t *testing.T) {
	client, _ := newFakeGitHub(t, func(_ *http.Request) (int, string, http.Header) {
		return http.StatusOK, `{"total_count":1,"items":[{"sha":"deadbeef","commit":{"message":"fix: bug"}}]}`, nil
	})
	r := newRecordingRegistrar()
	RegisterRepos(r, client)

	text, isErr := callTool(t, r, "search_commits", map[string]any{"query": "repo:o/r fix bug"})
	if isErr {
		t.Fatalf("unexpected error result: %s", text)
	}
	if !strings.Contains(text, "deadbeef") || !strings.Contains(text, "fix: bug") {
		t.Errorf("unexpected payload: %s", text)
	}
}

func TestListReleasesAndTagsSuccess(t *testing.T) {
	client, _ := newFakeGitHub(t, func(r *http.Request) (int, string, http.Header) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/releases"):
			return http.StatusOK, `[{"tag_name":"v1.0.0","name":"v1.0.0","draft":false,"prerelease":false}]`, nil
		case strings.HasSuffix(r.URL.Path, "/tags"):
			return http.StatusOK, `[{"name":"v1.0.0","commit":{"sha":"csha"}}]`, nil
		}
		return 0, "", nil
	})
	r := newRecordingRegistrar()
	RegisterRepos(r, client)

	text, isErr := callTool(t, r, "list_releases", map[string]any{"owner": "o", "repo": "r"})
	if isErr || !strings.Contains(text, "v1.0.0") {
		t.Errorf("list_releases: isErr=%v text=%s", isErr, text)
	}
	text, isErr = callTool(t, r, "list_tags", map[string]any{"owner": "o", "repo": "r"})
	if isErr || !strings.Contains(text, "csha") {
		t.Errorf("list_tags: isErr=%v text=%s", isErr, text)
	}
}

func TestGetLatestReleaseAndByTag(t *testing.T) {
	client, _ := newFakeGitHub(t, func(r *http.Request) (int, string, http.Header) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/releases/latest"):
			return http.StatusOK, `{"tag_name":"v2.0.0","name":"v2.0.0"}`, nil
		case strings.HasSuffix(r.URL.Path, "/releases/tags/v1.0.0"):
			return http.StatusOK, `{"tag_name":"v1.0.0","name":"v1.0.0"}`, nil
		}
		return 0, "", nil
	})
	r := newRecordingRegistrar()
	RegisterRepos(r, client)

	text, isErr := callTool(t, r, "get_latest_release", map[string]any{"owner": "o", "repo": "r"})
	if isErr || !strings.Contains(text, "v2.0.0") {
		t.Errorf("get_latest_release: isErr=%v text=%s", isErr, text)
	}
	text, isErr = callTool(t, r, "get_release_by_tag", map[string]any{"owner": "o", "repo": "r", "tag": "v1.0.0"})
	if isErr || !strings.Contains(text, "v1.0.0") {
		t.Errorf("get_release_by_tag: isErr=%v text=%s", isErr, text)
	}
}

func TestGetTagAnnotated(t *testing.T) {
	client, calls := newFakeGitHub(t, func(r *http.Request) (int, string, http.Header) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/git/ref/tags/v1.0.0"):
			return http.StatusOK, `{"ref":"refs/tags/v1.0.0","object":{"sha":"tagsha","type":"tag"}}`, nil
		case strings.HasSuffix(r.URL.Path, "/git/tags/tagsha"):
			return http.StatusOK, `{"sha":"tagsha","tag":"v1.0.0","message":"release notes","object":{"sha":"commitsha","type":"commit"}}`, nil
		}
		return 0, "", nil
	})
	r := newRecordingRegistrar()
	RegisterRepos(r, client)

	text, isErr := callTool(t, r, "get_tag", map[string]any{"owner": "o", "repo": "r", "tag": "v1.0.0"})
	if isErr {
		t.Fatalf("unexpected error result: %s", text)
	}
	if !strings.Contains(text, "release notes") || !strings.Contains(text, "tagsha") {
		t.Errorf("unexpected payload: %s", text)
	}
	all := strings.Join(*calls, "\n")
	if !strings.Contains(all, "/git/tags/tagsha") {
		t.Errorf("expected the tag object fetch, calls:\n%s", all)
	}
}

func TestGetTagLightweightSkipsTagObject(t *testing.T) {
	client, calls := newFakeGitHub(t, func(_ *http.Request) (int, string, http.Header) {
		return http.StatusOK, `{"ref":"refs/tags/v0.9.0","object":{"sha":"csha","type":"commit"}}`, nil
	})
	r := newRecordingRegistrar()
	RegisterRepos(r, client)

	text, isErr := callTool(t, r, "get_tag", map[string]any{"owner": "o", "repo": "r", "tag": "v0.9.0"})
	if isErr {
		t.Fatalf("unexpected error result: %s", text)
	}
	if !strings.Contains(text, `"type":"lightweight"`) || !strings.Contains(text, "csha") {
		t.Errorf("unexpected payload: %s", text)
	}
	for _, c := range *calls {
		if strings.Contains(c, "/git/tags/") {
			t.Errorf("lightweight tag must not fetch a tag object, calls: %v", *calls)
		}
	}
}

func TestListRepositoryCollaborators(t *testing.T) {
	client, calls := newFakeGitHub(t, func(_ *http.Request) (int, string, http.Header) {
		return http.StatusOK, `[{"login":"alice","permissions":{"push":true}}]`, nil
	})
	r := newRecordingRegistrar()
	RegisterRepos(r, client)

	text, isErr := callTool(t, r, "list_repository_collaborators", map[string]any{
		"owner": "o", "repo": "r", "affiliation": "all", "permission": "push",
	})
	if isErr {
		t.Fatalf("unexpected error result: %s", text)
	}
	if !strings.Contains(text, "alice") {
		t.Errorf("unexpected payload: %s", text)
	}
	if !strings.Contains((*calls)[0], "/collaborators") {
		t.Errorf("unexpected call: %v", *calls)
	}
}

func TestCreateRepository(t *testing.T) {
	client, calls := newFakeGitHub(t, func(_ *http.Request) (int, string, http.Header) {
		return http.StatusCreated, `{"name":"newrepo","full_name":"me/newrepo","private":true}`, nil
	})
	r := newRecordingRegistrar()
	RegisterRepos(r, client)

	text, isErr := callTool(t, r, "create_repository", map[string]any{
		"name": "newrepo", "private": true, "auto_init": true,
	})
	if isErr {
		t.Fatalf("unexpected error result: %s", text)
	}
	if !strings.Contains(text, "me/newrepo") {
		t.Errorf("unexpected payload: %s", text)
	}
	if len(*calls) != 1 || (*calls)[0] != "POST /api/v3/user/repos" {
		t.Errorf("unexpected calls: %v", *calls)
	}
}

func TestDeleteRepositoryRequiresMatchingConfirm(t *testing.T) {
	client, calls := newFakeGitHub(t, func(_ *http.Request) (int, string, http.Header) {
		return http.StatusNoContent, ``, nil
	})
	r := newRecordingRegistrar()
	RegisterRepos(r, client)

	text, isErr := callTool(t, r, "delete_repository", map[string]any{
		"owner": "o", "repo": "r", "confirm": "o/other",
	})
	if !isErr {
		t.Fatalf("mismatched confirm must fail, got: %s", text)
	}
	if !strings.Contains(text, "does not match") {
		t.Errorf("error should name the mismatch, got: %s", text)
	}
	if len(*calls) != 0 {
		t.Errorf("mismatched confirm must not reach the API, calls: %v", *calls)
	}

	text, isErr = callTool(t, r, "delete_repository", map[string]any{
		"owner": "o", "repo": "r", "confirm": "o/r",
	})
	if isErr {
		t.Fatalf("matching confirm must succeed, got: %s", text)
	}
	if !strings.Contains(text, `"deleted":"o/r"`) {
		t.Errorf("unexpected payload: %s", text)
	}
}

func TestForkRepositoryInProgress202(t *testing.T) {
	client, calls := newFakeGitHub(t, func(_ *http.Request) (int, string, http.Header) {
		return http.StatusAccepted, `{}`, nil
	})
	r := newRecordingRegistrar()
	RegisterRepos(r, client)

	text, isErr := callTool(t, r, "fork_repository", map[string]any{"owner": "o", "repo": "r"})
	if isErr {
		t.Fatalf("unexpected error result: %s", text)
	}
	if !strings.Contains(text, "fork_in_progress") {
		t.Errorf("202 must be reported as in progress, got: %s", text)
	}
	if len(*calls) != 1 || (*calls)[0] != "POST /api/v3/repos/o/r/forks" {
		t.Errorf("unexpected calls: %v", *calls)
	}
}
