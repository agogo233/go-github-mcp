package tools

import (
	"context"
	"fmt"

	"github.com/google/go-github/v72/github"
	mcp "github.com/mark3labs/mcp-go/mcp"
)

var gistFilesSchema = mcp.Items(map[string]any{
	"type": "object",
	"properties": map[string]any{
		"filename": map[string]any{"type": "string"},
		"content":  map[string]any{"type": "string"},
	},
	"required": []string{"filename", "content"},
})

// parseGistFiles converts the files array into the map go-github expects.
func parseGistFiles(req mcp.CallToolRequest, key string) (map[github.GistFilename]github.GistFile, error) {
	raw, ok := req.GetArguments()[key]
	if !ok {
		return nil, nil
	}
	items, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("%s must be an array of {filename, content} objects", key)
	}
	out := make(map[github.GistFilename]github.GistFile, len(items))
	for i, item := range items {
		obj, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%s[%d] must be an object", key, i)
		}
		name, _ := obj["filename"].(string)
		content, _ := obj["content"].(string)
		if name == "" {
			return nil, fmt.Errorf("%s[%d].filename is required", key, i)
		}
		out[github.GistFilename(name)] = github.GistFile{Content: github.Ptr(content)}
	}
	return out, nil
}

// RegisterGists registers the "gists" toolset.
func RegisterGists(r Registrar, client *github.Client) {
	listGists := mcp.NewTool("list_gists",
		mcp.WithDescription("List gists of the authenticated user (or a given username)."),
		mcp.WithString("username", mcp.Description("Username whose gists to list; empty means the authenticated user")),
		mcp.WithNumber("page", mcp.Description("Page number (starts at 1)")),
		mcp.WithNumber("per_page", mcp.Description("Results per page (default 30, max 100)")),
	)
	r.Add("gists", false, listGists, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		gists, _, err := client.Gists.List(ctx, req.GetString("username", ""), &github.GistListOptions{
			ListOptions: *listOptions(req),
		})
		if err != nil {
			return errResult("list gists", err)
		}
		return jsonResult(gists)
	})

	getGist := mcp.NewTool("get_gist",
		mcp.WithDescription("Get a single gist including file contents."),
		mcp.WithString("gist_id", mcp.Required(), mcp.Description("Gist ID")),
	)
	r.Add("gists", false, getGist, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		gistID, err := req.RequireString("gist_id")
		if err != nil {
			return errResult("get gist", err)
		}
		gist, _, err := client.Gists.Get(ctx, gistID)
		if err != nil {
			return errResult("get gist", err)
		}
		return jsonResult(gist)
	})

	createGist := mcp.NewTool("create_gist",
		mcp.WithDescription("Create a new gist with one or more files."),
		mcp.WithString("description", mcp.Description("Gist description")),
		mcp.WithBoolean("public", mcp.Description("Make the gist public (default: secret)")),
		mcp.WithArray("files", mcp.Required(),
			mcp.Description("Array of {\"filename\": \"a.txt\", \"content\": \"text\"} objects"),
			gistFilesSchema,
		),
	)
	r.Add("gists", true, createGist, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		files, err := parseGistFiles(req, "files")
		if err != nil {
			return errResult("create gist", err)
		}
		if len(files) == 0 {
			return mcp.NewToolResultError("files must contain at least one entry"), nil
		}
		gist := &github.Gist{
			Files:       files,
			Public:      github.Ptr(req.GetBool("public", false)),
			Description: github.Ptr(req.GetString("description", "")),
		}
		created, _, err := client.Gists.Create(ctx, gist)
		if err != nil {
			return errResult("create gist", err)
		}
		return jsonResult(created)
	})

	updateGist := mcp.NewTool("update_gist",
		mcp.WithDescription("Update a gist's description and/or files. Files set to empty content are deleted."),
		mcp.WithString("gist_id", mcp.Required(), mcp.Description("Gist ID")),
		mcp.WithString("description", mcp.Description("New description; omit or pass null to keep the current description unchanged")),
		mcp.WithArray("files",
			mcp.Description("Files to upsert: array of {\"filename\": \"a.txt\", \"content\": \"text\"}; content \"\" deletes the file"),
			gistFilesSchema,
		),
	)
	r.Add("gists", true, updateGist, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		gistID, err := req.RequireString("gist_id")
		if err != nil {
			return errResult("update gist", err)
		}
		gist := &github.Gist{}
		if v, ok := req.GetArguments()["description"]; ok {
			if s, ok := v.(string); ok {
				gist.Description = github.Ptr(s)
			}
		}
		files, err := parseGistFiles(req, "files")
		if err != nil {
			return errResult("update gist", err)
		}
		if len(files) > 0 {
			gist.Files = files
		}
		updated, _, err := client.Gists.Edit(ctx, gistID, gist)
		if err != nil {
			return errResult("update gist", err)
		}
		return jsonResult(updated)
	})

	deleteGist := mcp.NewTool("delete_gist",
		mcp.WithDescription("Delete a gist permanently."),
		mcp.WithString("gist_id", mcp.Required(), mcp.Description("Gist ID")),
	)
	r.Add("gists", true, deleteGist, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		gistID, err := req.RequireString("gist_id")
		if err != nil {
			return errResult("delete gist", err)
		}
		if _, err := client.Gists.Delete(ctx, gistID); err != nil {
			return errResult("delete gist", err)
		}
		return jsonResult(map[string]any{"deleted": gistID})
	})
}
