package tools

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"reflect"
	"strconv"
	"strings"

	"github.com/google/go-github/v72/github"
	mcp "github.com/mark3labs/mcp-go/mcp"
)

// jsonResult marshals v and wraps it as a text tool result. Nil slices are
// normalized to empty JSON arrays so callers never receive `null` for
// "no data".
func jsonResult(v any) (*mcp.CallToolResult, error) {
	data, err := json.Marshal(normalizeNilSlice(v))
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to encode result: %v", err)), nil
	}
	return mcp.NewToolResultText(string(data)), nil
}

func normalizeNilSlice(v any) any {
	if v == nil {
		return []any{}
	}
	rv := reflect.ValueOf(v)
	if rv.Kind() == reflect.Slice && rv.IsNil() {
		return reflect.MakeSlice(rv.Type(), 0, 0).Interface()
	}
	return v
}

// errResult converts a GitHub API failure into a tool error result. The
// upstream message is kept verbatim (it already carries the HTTP status) and
// augmented with a short actionable hint so the caller can decide what to do
// next instead of retrying blindly.
func errResult(action string, err error) (*mcp.CallToolResult, error) {
	msg := fmt.Sprintf("failed to %s: %v%s", action, err, errorHint(err))
	return mcp.NewToolResultError(msg), nil
}

// errorHint returns a short remediation hint for common GitHub API failure
// shapes: not-found vs. forbidden vs. validation failure vs. rate limiting.
func errorHint(err error) string {
	var eresp *github.ErrorResponse
	if errors.As(err, &eresp) && eresp.Response != nil {
		switch eresp.Response.StatusCode {
		case http.StatusNotFound:
			return " (hint: not found, or the token lacks access to this private resource)"
		case http.StatusUnauthorized:
			return " (hint: the token was rejected; check that it is valid and not expired)"
		case http.StatusForbidden:
			return " (hint: the token lacks permission for this action, or a rate limit applies)"
		case http.StatusUnprocessableEntity:
			if len(eresp.Errors) > 0 {
				return fmt.Sprintf(" (hint: validation failed: %s)", formatFieldErrors(eresp.Errors))
			}
			return " (hint: validation failed; check the supplied field values)"
		}
	}
	var rle *github.RateLimitError
	if errors.As(err, &rle) {
		return " (hint: primary rate limit exhausted; wait until the reset time shown above)"
	}
	var arle *github.AbuseRateLimitError
	if errors.As(err, &arle) {
		return " (hint: secondary rate limit triggered; wait before retrying)"
	}
	return ""
}

func formatFieldErrors(errs []github.Error) string {
	parts := make([]string, 0, len(errs))
	for _, e := range errs {
		parts = append(parts, fmt.Sprintf("%s %s %s", e.Resource, e.Field, e.Code))
	}
	return strings.Join(parts, "; ")
}

// jsonResultWithPage marshals v as the first content block exactly like
// jsonResult and appends a second content block carrying pagination info
// parsed from the GitHub response. Keeping the payload in the first block
// preserves the pre-existing result shape for clients; the extra block tells
// callers whether more pages exist.
func jsonResultWithPage(resp *github.Response, v any) (*mcp.CallToolResult, error) {
	res, err := jsonResult(v)
	if err != nil {
		return res, err
	}
	if resp == nil {
		return res, nil
	}
	meta := map[string]any{"has_next": resp.NextPage != 0}
	if resp.NextPage != 0 {
		meta["next_page"] = resp.NextPage
	}
	encoded, err := json.Marshal(meta)
	if err != nil {
		return res, nil
	}
	res.Content = append(res.Content, mcp.TextContent{Type: "text", Text: string(encoded)})
	return res, nil
}

// pagination reads the common page/per_page arguments with sane bounds.
func pagination(req mcp.CallToolRequest) (page, perPage int) {
	page = req.GetInt("page", 1)
	if page < 1 {
		page = 1
	}
	perPage = req.GetInt("per_page", 30)
	if perPage < 1 {
		perPage = 30
	}
	if perPage > 100 {
		perPage = 100
	}
	return page, perPage
}

// listOptions builds go-github ListOptions from common pagination arguments.
func listOptions(req mcp.CallToolRequest) *github.ListOptions {
	page, perPage := pagination(req)
	return &github.ListOptions{Page: page, PerPage: perPage}
}

// ownerRepo extracts the required owner/repo argument pair.
func ownerRepo(req mcp.CallToolRequest) (string, string, error) {
	owner, err := req.RequireString("owner")
	if err != nil {
		return "", "", err
	}
	repo, err := req.RequireString("repo")
	if err != nil {
		return "", "", err
	}
	return strings.TrimSpace(owner), strings.TrimSpace(repo), nil
}

func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// splitCommaList parses a comma-separated argument into a clean slice.
func splitCommaList(v string) []string {
	if v == "" {
		return nil
	}
	var out []string
	for _, item := range strings.Split(v, ",") {
		if s := strings.TrimSpace(item); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// strPtrIfSet returns a pointer to v when set, else nil (for optional filters).
func strPtrIfSet(v string) *string {
	if v == "" {
		return nil
	}
	return github.Ptr(v)
}

// int64Arg reads a required int64 argument (workflow/run/job IDs). It inspects
// the raw argument instead of mcp-go's int helpers so large IDs survive on
// 32-bit platforms where int is only 32 bits.
func int64Arg(req mcp.CallToolRequest, key, action string) (int64, *mcp.CallToolResult) {
	id, ok := int64FromArg(req, key)
	if !ok {
		res, _ := errResult(action, fmt.Errorf("argument %q is required and must be an integer ID", key))
		return 0, res
	}
	return id, nil
}

// int64FromArg extracts an integer ID from a tool argument without
// platform-dependent narrowing: JSON numbers arrive as float64 and must not
// pass through int on 32-bit builds. Accepts ints, integral floats, and
// numeric strings; ok is false when the argument is missing or invalid.
func int64FromArg(req mcp.CallToolRequest, key string) (int64, bool) {
	v, ok := req.GetArguments()[key]
	if !ok {
		return 0, false
	}
	switch n := v.(type) {
	case int:
		return int64(n), true
	case float64:
		// 2^63 is exactly representable as a float64; values at or above it
		// would overflow int64 on conversion.
		if n != math.Trunc(n) || n < 0 || n >= float64(1<<63) {
			return 0, false
		}
		return int64(n), true
	case string:
		id, err := strconv.ParseInt(strings.TrimSpace(n), 10, 64)
		if err != nil || id < 0 {
			return 0, false
		}
		return id, true
	default:
		return 0, false
	}
}

// intIDArg reads a required integer entity argument (issue/PR numbers,
// comment and alert IDs) with the same platform-safe parsing as int64Arg,
// returning an int for the go-github APIs that take entity numbers as int.
// action names the operation for error messages.
func intIDArg(req mcp.CallToolRequest, key, action string) (int, *mcp.CallToolResult) {
	id, ok := int64FromArg(req, key)
	if !ok {
		res, _ := errResult(action, fmt.Errorf("argument %q is required and must be an integer ID", key))
		return 0, res
	}
	if id > int64(math.MaxInt) {
		res, _ := errResult(action, fmt.Errorf("argument %q exceeds the integer range", key))
		return 0, res
	}
	return int(id), nil
}

// isEmptyArg reports whether an argument is present but empty (empty string
// or nil), which callers usually treat the same as "not provided".
func isEmptyArg(v any) bool {
	if v == nil {
		return true
	}
	s, ok := v.(string)
	return ok && strings.TrimSpace(s) == ""
}

// validState reports whether v is one of the allowed state values. Toolsets
// use it to reject invalid filters early with a message that names the
// accepted values instead of relaying a less specific API error later.
func validState(v string, allowed ...string) bool {
	for _, a := range allowed {
		if v == a {
			return true
		}
	}
	return false
}

// stringListArg converts a raw JSON argument (an array of strings, as
// decoded from the MCP call arguments) into a []string, skipping non-string
// items. ok reports whether the value was an array at all; presence matters
// for callers where an empty array is meaningful (e.g. clearing all labels).
func stringListArg(v any) (out []string, ok bool) {
	arr, ok := v.([]any)
	if !ok {
		return nil, false
	}
	out = make([]string, 0, len(arr))
	for _, item := range arr {
		if s, isStr := item.(string); isStr {
			out = append(out, s)
		}
	}
	return out, true
}

// withRepoArgs declares the standard owner/repo argument pair.
func withRepoArgs() mcp.ToolOption {
	return func(t *mcp.Tool) {
		mcp.WithString("owner", mcp.Required(), mcp.Description("Repository owner"))(t)
		mcp.WithString("repo", mcp.Required(), mcp.Description("Repository name"))(t)
	}
}

// withPagination declares the standard page/per_page arguments.
func withPagination() mcp.ToolOption {
	return func(t *mcp.Tool) {
		mcp.WithNumber("page", mcp.Description("Page number (starts at 1)"))(t)
		mcp.WithNumber("per_page", mcp.Description("Results per page (default 30, max 100)"))(t)
	}
}
