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
