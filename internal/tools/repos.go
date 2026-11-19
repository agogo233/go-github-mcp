package tools

import (
	"context"

	"github.com/google/go-github/v72/github"
	mcp "github.com/mark3labs/mcp-go/mcp"
)

// RegisterRepos registers the "repos" toolset.
func RegisterRepos(r Registrar, client *github.Client) {
	getRepository := mcp.NewTool("get_repository",
		mcp.WithDescription("Get metadata for a single repository."),
		withRepoArgs(),
	)
	r.Add("repos", false, getRepository, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		owner, repo, err := ownerRepo(req)
		if err != nil {
			return errResult("get repository", err)
		}
		repository, _, err := client.Repositories.Get(ctx, owner, repo)
		if err != nil {
			return errResult("get repository", err)
		}
		return jsonResult(repository)
	})

	searchRepositories := mcp.NewTool("search_repositories",
		mcp.WithDescription("Search GitHub repositories with the GitHub search syntax."),
		mcp.WithString("query", mcp.Required(), mcp.Description("Search query, e.g. 'go mcp server stars:>100'")),
		withPagination(),
	)
	r.Add("repos", false, searchRepositories, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		q, err := req.RequireString("query")
		if err != nil {
			return errResult("search repositories", err)
		}
		opts := &github.SearchOptions{ListOptions: *listOptions(req)}
		result, resp, err := client.Search.Repositories(ctx, q, opts)
		if err != nil {
			return errResult("search repositories", err)
		}
		return jsonResultWithPage(resp, result)
	})

	getFileContents := mcp.NewTool("get_file_contents",
		mcp.WithDescription("Get the contents of a file or directory listing inside a repository. "+
			"Large files (>1MB) may return empty content; use get_blob with the file SHA instead."),
		withRepoArgs(),
		mcp.WithString("path", mcp.Required(), mcp.Description("File or directory path, e.g. 'src/main.go' or 'docs'")),
		mcp.WithString("ref", mcp.Description("Branch, tag or commit SHA; defaults to the default branch")),
	)
	r.Add("repos", false, getFileContents, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		owner, repo, err := ownerRepo(req)
		if err != nil {
			return errResult("get file contents", err)
		}
		path, err := req.RequireString("path")
		if err != nil {
			return errResult("get file contents", err)
		}
		opts := &github.RepositoryContentGetOptions{Ref: req.GetString("ref", "")}
		file, dir, _, err := client.Repositories.GetContents(ctx, owner, repo, path, opts)
		if err != nil {
			return errResult("get file contents", err)
		}
		if file != nil {
			content, err := file.GetContent()
			if err != nil {
				return errResult("decode file contents", err)
			}
			result := map[string]any{
				"type":     "file",
				"path":     file.Path,
				"sha":      file.SHA,
				"size":     file.Size,
				"encoding": "utf-8",
				"content":  content,
			}
			if content == "" {
				result["note"] = "content empty or truncated (file likely >1MB); fetch the blob with get_blob using this sha"
			}
			return jsonResult(result)
		}
		return jsonResult(map[string]any{"type": "directory", "entries": dir})
	})

	createOrUpdateFile := mcp.NewTool("create_or_update_file",
		mcp.WithDescription("Create or update a single text file in a repository and commit it. "+
			"For multiple files in one commit use push_files."),
		withRepoArgs(),
		mcp.WithString("path", mcp.Required(), mcp.Description("File path inside the repository")),
		mcp.WithString("content", mcp.Required(), mcp.Description("Full text content of the file (UTF-8)")),
		mcp.WithString("message", mcp.Required(), mcp.Description("Commit message")),
		mcp.WithString("branch", mcp.Description("Branch to commit to; defaults to the default branch")),
		mcp.WithString("sha", mcp.Description("Blob SHA of the file being replaced (required when updating an existing file)")),
	)
	r.Add("repos", true, createOrUpdateFile, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		owner, repo, err := ownerRepo(req)
		if err != nil {
			return errResult("create or update file", err)
		}
		path, err := req.RequireString("path")
		if err != nil {
			return errResult("create or update file", err)
		}
		content, err := req.RequireString("content")
		if err != nil {
			return errResult("create or update file", err)
		}
		message, err := req.RequireString("message")
		if err != nil {
			return errResult("create or update file", err)
		}
		opts := &github.RepositoryContentFileOptions{
			Message: &message,
			Content: []byte(content),
		}
		if v := req.GetString("branch", ""); v != "" {
			opts.Branch = &v
		}
		if v := req.GetString("sha", ""); v != "" {
			opts.SHA = &v
		}
		result, _, err := client.Repositories.CreateFile(ctx, owner, repo, path, opts)
		if err != nil {
			return errResult("create or update file", err)
		}
		return jsonResult(result)
	})

	deleteFile := mcp.NewTool("delete_file",
		mcp.WithDescription("Delete a file from a repository and commit the deletion."),
		withRepoArgs(),
		mcp.WithString("path", mcp.Required(), mcp.Description("File path to delete")),
		mcp.WithString("message", mcp.Required(), mcp.Description("Commit message")),
		mcp.WithString("branch", mcp.Description("Branch to commit to; defaults to the default branch")),
		mcp.WithString("sha", mcp.Description("Blob SHA of the file; resolved automatically when omitted")),
	)
	r.Add("repos", true, deleteFile, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		owner, repo, err := ownerRepo(req)
		if err != nil {
			return errResult("delete file", err)
		}
		path, err := req.RequireString("path")
		if err != nil {
			return errResult("delete file", err)
		}
		message, err := req.RequireString("message")
		if err != nil {
			return errResult("delete file", err)
		}
		opts := &github.RepositoryContentFileOptions{Message: &message}
		if v := req.GetString("branch", ""); v != "" {
			opts.Branch = &v
		}
		sha := req.GetString("sha", "")
		if sha == "" {
			// Resolve the current blob SHA so callers only need the path.
			ref := opts.Branch
			file, _, _, err := client.Repositories.GetContents(ctx, owner, repo, path,
				&github.RepositoryContentGetOptions{Ref: derefString(ref)})
			if err != nil {
				return errResult("resolve file SHA before delete", err)
			}
			if file == nil || file.SHA == nil {
				return mcp.NewToolResultError("path is a directory or has no blob SHA; cannot delete"), nil
			}
			sha = *file.SHA
		}
		opts.SHA = &sha
		result, _, err := client.Repositories.DeleteFile(ctx, owner, repo, path, opts)
		if err != nil {
			return errResult("delete file", err)
		}
		return jsonResult(result)
	})
}
