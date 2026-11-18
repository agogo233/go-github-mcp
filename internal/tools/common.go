package tools

import (
	"encoding/json"
	"fmt"

	"github.com/google/go-github/v72/github"
	mcp "github.com/mark3labs/mcp-go/mcp"
)

// jsonResult marshals v and wraps it as a text tool result.
func jsonResult(v any) (*mcp.CallToolResult, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to encode result: %v", err)), nil
	}
	return mcp.NewToolResultText(string(data)), nil
}

// errResult converts a GitHub API failure into a tool error result.
func errResult(action string, err error) (*mcp.CallToolResult, error) {
	return mcp.NewToolResultError(fmt.Sprintf("failed to %s: %v", action, err)), nil
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
