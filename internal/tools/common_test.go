package tools

import (
	"testing"

	mcp "github.com/mark3labs/mcp-go/mcp"
)

func reqWithArgs(args map[string]any) mcp.CallToolRequest {
	var req mcp.CallToolRequest
	req.Params.Arguments = args
	return req
}

func TestOwnerRepo(t *testing.T) {
	owner, repo, err := ownerRepo(reqWithArgs(map[string]any{"owner": " octo ", "repo": " hello "}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if owner != "octo" || repo != "hello" {
		t.Errorf("ownerRepo = %q, %q; want octo, hello (trimmed)", owner, repo)
	}

	if _, _, err := ownerRepo(reqWithArgs(map[string]any{"repo": "hello"})); err == nil {
		t.Error("expected error when owner is missing")
	}
	if _, _, err := ownerRepo(reqWithArgs(map[string]any{"owner": "octo"})); err == nil {
		t.Error("expected error when repo is missing")
	}
	if _, _, err := ownerRepo(reqWithArgs(map[string]any{"owner": 123, "repo": "hello"})); err == nil {
		t.Error("expected error when owner is not a string")
	}
}

func TestPaginationClamps(t *testing.T) {
	tests := []struct {
		name        string
		args        map[string]any
		wantPage    int
		wantPerPage int
	}{
		{"defaults", map[string]any{}, 1, 30},
		{"page below one clamps to one", map[string]any{"page": 0}, 1, 30},
		{"negative per page clamps to default", map[string]any{"per_page": -5}, 1, 30},
		{"per page above hundred clamps", map[string]any{"page": 3, "per_page": 1000}, 3, 100},
		{"numeric strings accepted", map[string]any{"page": "4", "per_page": "7"}, 4, 7},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			page, perPage := pagination(reqWithArgs(tt.args))
			if page != tt.wantPage || perPage != tt.wantPerPage {
				t.Errorf("pagination = %d, %d; want %d, %d", page, perPage, tt.wantPage, tt.wantPerPage)
			}
		})
	}
}

func TestListOptions(t *testing.T) {
	opts := listOptions(reqWithArgs(map[string]any{"page": 2, "per_page": 50}))
	if opts.Page != 2 || opts.PerPage != 50 {
		t.Errorf("ListOptions = %+v; want Page=2 PerPage=50", opts)
	}
}

func TestInt64Arg(t *testing.T) {
	id, res := int64Arg(reqWithArgs(map[string]any{"id": float64(42)}), "id", "fetch widget")
	if res != nil {
		t.Fatalf("unexpected error result: %v", res)
	}
	if id != 42 {
		t.Errorf("id = %d, want 42", id)
	}

	if _, res := int64Arg(reqWithArgs(map[string]any{}), "id", "fetch widget"); res == nil {
		t.Error("expected error result when argument is missing")
	}
	if _, res := int64Arg(reqWithArgs(map[string]any{"id": "abc"}), "id", "fetch widget"); res == nil {
		t.Error("expected error result when argument is not numeric")
	}
}
