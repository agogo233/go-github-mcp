package tools

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
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
