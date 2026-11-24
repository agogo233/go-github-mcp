package tools

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// A blob within the cap is returned as-is.
func TestGetBlobReturnsSmallBlob(t *testing.T) {
	client, _ := newFakeGitHub(t, func(_ *http.Request) (int, string, http.Header) {
		return http.StatusOK, `{"sha":"abc","content":"aGk=","encoding":"base64","size":2}`, nil
	})
	r := newRecordingRegistrar()
	RegisterGit(r, client)

	text, isErr := callTool(t, r, "get_blob", map[string]any{"owner": "o", "repo": "r", "sha": "abc"})
	if isErr {
		t.Fatalf("unexpected error result: %s", text)
	}
	if !strings.Contains(text, "aGk=") {
		t.Errorf("blob payload missing, got: %s", text)
	}
}

// A blob above the cap must be rejected with its size and a pointer to the
// alternatives, instead of flooding the agent context (and memory).
func TestGetBlobRejectsOversizedBlob(t *testing.T) {
	client, _ := newFakeGitHub(t, func(_ *http.Request) (int, string, http.Header) {
		body := fmt.Sprintf(`{"sha":"abc","content":"%s","encoding":"base64","size":%d}`,
			strings.Repeat("A", 16), maxBlobBytes+1)
		return http.StatusOK, body, nil
	})
	r := newRecordingRegistrar()
	RegisterGit(r, client)

	text, isErr := callTool(t, r, "get_blob", map[string]any{"owner": "o", "repo": "r", "sha": "abc"})
	if !isErr {
		t.Fatalf("expected error result, got: %s", text)
	}
	for _, want := range []string{"get_blob limit", "get_tree"} {
		if !strings.Contains(text, want) {
			t.Errorf("error should mention %q, got: %s", want, text)
		}
	}
}

func TestParseFileEntriesDefaultsMode(t *testing.T) {
	req := reqWithArgs(map[string]any{"files": []any{
		map[string]any{"path": "a.txt", "content": "x"},
		map[string]any{"path": "b.sh", "content": "y", "mode": "100755"},
	}})
	entries, err := parseFileEntries(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if entries[0].Mode != "100644" {
		t.Errorf("default mode = %q, want 100644", entries[0].Mode)
	}
	if entries[1].Mode != "100755" {
		t.Errorf("explicit mode = %q, want 100755", entries[1].Mode)
	}
}

func TestParseFileEntriesRejectsInvalidMode(t *testing.T) {
	req := reqWithArgs(map[string]any{"files": []any{
		map[string]any{"path": "a.txt", "content": "x", "mode": "040000"},
	}})
	if _, err := parseFileEntries(req); err == nil || !strings.Contains(err.Error(), "100644") {
		t.Errorf("expected invalid-mode error naming allowed modes, got %v", err)
	}
}

func TestParseFileEntriesRejectsDuplicatePaths(t *testing.T) {
	req := reqWithArgs(map[string]any{"files": []any{
		map[string]any{"path": "a.txt", "content": "x"},
		map[string]any{"path": "a.txt", "content": "y"},
	}})
	if _, err := parseFileEntries(req); err == nil || !strings.Contains(err.Error(), "more than once") {
		t.Errorf("expected duplicate-path error, got %v", err)
	}
}

// The JSON comparison is trimmed: the redundant base/merge-base commit
// objects are dropped and only the merge-base SHA survives.
func TestCompareCommitsReturnsTrimmedJSON(t *testing.T) {
	client, calls := newFakeGitHub(t, func(_ *http.Request) (int, string, http.Header) {
		return http.StatusOK, `{"status":"diverged","ahead_by":2,"behind_by":3,"total_commits":2,
			"base_commit":{"sha":"base","commit":{"message":"base"}},
			"merge_base_commit":{"sha":"merge","commit":{"message":"merge"}},
			"commits":[{"sha":"c1","commit":{"message":"one"}},{"sha":"c2","commit":{"message":"two"}}],
			"files":[{"filename":"a.go","status":"modified"}]}`, nil
	})
	r := newRecordingRegistrar()
	RegisterGit(r, client)

	text, isErr := callTool(t, r, "compare_commits", map[string]any{
		"owner": "o", "repo": "r", "base": "main", "head": "feature",
	})
	if isErr {
		t.Fatalf("unexpected error result: %s", text)
	}
	for _, want := range []string{`"ahead_by":2`, `"behind_by":3`, `"status":"diverged"`, `"merge_base_sha":"merge"`, `"sha":"c1"`, `"filename":"a.go"`} {
		if !strings.Contains(text, want) {
			t.Errorf("payload missing %s, got: %s", want, text)
		}
	}
	for _, unwanted := range []string{"base_commit", "merge_base_commit"} {
		if strings.Contains(text, unwanted) {
			t.Errorf("trimmed payload must not contain %s, got: %s", unwanted, text)
		}
	}
	if len(*calls) != 1 || !strings.Contains((*calls)[0], "/compare/main...feature") {
		t.Errorf("unexpected request path: %v", *calls)
	}
}

// Raw formats pass the diff/patch text through unchanged.
func TestCompareCommitsRawFormats(t *testing.T) {
	for _, tt := range []struct{ format, body string }{
		{"diff", "diff --git a/x.go b/x.go\n--- a/x.go\n+++ b/x.go\n"},
		{"patch", "From abc... Mon Sep 17 00:00:00 2001\nSubject: one\n"},
	} {
		t.Run(tt.format, func(t *testing.T) {
			client, _ := newFakeGitHub(t, func(_ *http.Request) (int, string, http.Header) {
				return http.StatusOK, tt.body, nil
			})
			r := newRecordingRegistrar()
			RegisterGit(r, client)

			text, isErr := callTool(t, r, "compare_commits", map[string]any{
				"owner": "o", "repo": "r", "base": "main", "head": "feature", "format": tt.format,
			})
			if isErr {
				t.Fatalf("unexpected error result: %s", text)
			}
			if text != tt.body {
				t.Errorf("raw %s not passed through, got: %q", tt.format, text)
			}
		})
	}
}

func TestCompareCommitsRequiresBaseAndHead(t *testing.T) {
	client, _ := newFakeGitHub(t, func(_ *http.Request) (int, string, http.Header) {
		return http.StatusOK, `{}`, nil
	})
	r := newRecordingRegistrar()
	RegisterGit(r, client)

	for _, tt := range []struct {
		name, missing string
		args          map[string]any
	}{
		{"missing base", "base", map[string]any{"owner": "o", "repo": "r", "head": "feature"}},
		{"missing head", "head", map[string]any{"owner": "o", "repo": "r", "base": "main"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			text, isErr := callTool(t, r, "compare_commits", tt.args)
			if !isErr || !strings.Contains(text, tt.missing) {
				t.Errorf("expected error naming %s, got (isErr=%v): %s", tt.missing, isErr, text)
			}
		})
	}
}

func TestCompareCommitsRejectsInvalidFormat(t *testing.T) {
	client, _ := newFakeGitHub(t, func(_ *http.Request) (int, string, http.Header) {
		return http.StatusOK, `{}`, nil
	})
	r := newRecordingRegistrar()
	RegisterGit(r, client)

	text, isErr := callTool(t, r, "compare_commits", map[string]any{
		"owner": "o", "repo": "r", "base": "main", "head": "feature", "format": "xml",
	})
	if !isErr || !strings.Contains(text, "json, diff or patch") {
		t.Errorf("expected invalid-format error naming allowed values, got (isErr=%v): %s", isErr, text)
	}
}

// A JSON comparison above the cap must be rejected with remediation hints
// instead of flooding the agent context.
func TestCompareCommitsRejectsOversizedJSON(t *testing.T) {
	client, _ := newFakeGitHub(t, func(_ *http.Request) (int, string, http.Header) {
		body := fmt.Sprintf(`{"status":"ahead","ahead_by":1,"commits":[{"sha":"c1"}],
			"files":[{"filename":"big.txt","patch":"%s"}]}`, strings.Repeat("A", maxCompareBytes))
		return http.StatusOK, body, nil
	})
	r := newRecordingRegistrar()
	RegisterGit(r, client)

	text, isErr := callTool(t, r, "compare_commits", map[string]any{
		"owner": "o", "repo": "r", "base": "main", "head": "feature",
	})
	if !isErr || !strings.Contains(text, "above the") || !strings.Contains(text, "format=diff") {
		t.Errorf("expected oversized-JSON error with hints, got (isErr=%v): %s", isErr, text)
	}
}

func TestCompareCommitsRejectsOversizedDiff(t *testing.T) {
	client, _ := newFakeGitHub(t, func(_ *http.Request) (int, string, http.Header) {
		return http.StatusOK, strings.Repeat("d", maxCompareBytes+1), nil
	})
	r := newRecordingRegistrar()
	RegisterGit(r, client)

	text, isErr := callTool(t, r, "compare_commits", map[string]any{
		"owner": "o", "repo": "r", "base": "main", "head": "feature", "format": "diff",
	})
	if !isErr || !strings.Contains(text, "MiB limit") {
		t.Errorf("expected oversized-diff error, got (isErr=%v): %s", isErr, text)
	}
}
