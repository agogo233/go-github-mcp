package tools

import (
	"context"
	"errors"
	"fmt"

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

	searchCode := mcp.NewTool("search_code",
		mcp.WithDescription("Search code across GitHub with the GitHub search syntax. "+
			"Scope the search with qualifiers like repo:owner/name or org:owner."),
		mcp.WithString("query", mcp.Required(), mcp.Description("Search query, e.g. 'repo:owner/name TODO'")),
		withPagination(),
	)
	r.Add("repos", false, searchCode, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		q, err := req.RequireString("query")
		if err != nil {
			return errResult("search code", err)
		}
		result, resp, err := client.Search.Code(ctx, q, &github.SearchOptions{ListOptions: *listOptions(req)})
		if err != nil {
			return errResult("search code", err)
		}
		return jsonResultWithPage(resp, result)
	})

	searchCommits := mcp.NewTool("search_commits",
		mcp.WithDescription("Search commits with the GitHub search syntax (matches commit messages and metadata; "+
			"history searches are limited to the default branch)."),
		mcp.WithString("query", mcp.Required(), mcp.Description("Search query, e.g. 'repo:owner/name fix bug'")),
		withPagination(),
	)
	r.Add("repos", false, searchCommits, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		q, err := req.RequireString("query")
		if err != nil {
			return errResult("search commits", err)
		}
		result, resp, err := client.Search.Commits(ctx, q, &github.SearchOptions{ListOptions: *listOptions(req)})
		if err != nil {
			return errResult("search commits", err)
		}
		return jsonResultWithPage(resp, result)
	})

	listReleases := mcp.NewTool("list_releases",
		mcp.WithDescription("List releases of a repository, newest first."),
		withRepoArgs(),
		withPagination(),
	)
	r.Add("repos", false, listReleases, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		owner, repo, err := ownerRepo(req)
		if err != nil {
			return errResult("list releases", err)
		}
		releases, resp, err := client.Repositories.ListReleases(ctx, owner, repo, listOptions(req))
		if err != nil {
			return errResult("list releases", err)
		}
		return jsonResultWithPage(resp, releases)
	})

	getLatestRelease := mcp.NewTool("get_latest_release",
		mcp.WithDescription("Get the latest published release of a repository (the newest non-prerelease, non-draft release)."),
		withRepoArgs(),
	)
	r.Add("repos", false, getLatestRelease, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		owner, repo, err := ownerRepo(req)
		if err != nil {
			return errResult("get latest release", err)
		}
		release, _, err := client.Repositories.GetLatestRelease(ctx, owner, repo)
		if err != nil {
			return errResult("get latest release", err)
		}
		return jsonResult(release)
	})

	getReleaseByTag := mcp.NewTool("get_release_by_tag",
		mcp.WithDescription("Get a release by its tag name."),
		withRepoArgs(),
		mcp.WithString("tag", mcp.Required(), mcp.Description("Tag name, e.g. v1.2.0")),
	)
	r.Add("repos", false, getReleaseByTag, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		owner, repo, err := ownerRepo(req)
		if err != nil {
			return errResult("get release by tag", err)
		}
		tag, err := req.RequireString("tag")
		if err != nil {
			return errResult("get release by tag", err)
		}
		release, _, err := client.Repositories.GetReleaseByTag(ctx, owner, repo, tag)
		if err != nil {
			return errResult("get release by tag", err)
		}
		return jsonResult(release)
	})

	listTags := mcp.NewTool("list_tags",
		mcp.WithDescription("List tags of a repository."),
		withRepoArgs(),
		withPagination(),
	)
	r.Add("repos", false, listTags, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		owner, repo, err := ownerRepo(req)
		if err != nil {
			return errResult("list tags", err)
		}
		tags, resp, err := client.Repositories.ListTags(ctx, owner, repo, listOptions(req))
		if err != nil {
			return errResult("list tags", err)
		}
		return jsonResultWithPage(resp, tags)
	})

	getTag := mcp.NewTool("get_tag",
		mcp.WithDescription("Get tag details. Annotated tags return the full tag object; "+
			"lightweight tags are reported with their commit SHA."),
		withRepoArgs(),
		mcp.WithString("tag", mcp.Required(), mcp.Description("Tag name, e.g. v1.2.0")),
	)
	r.Add("repos", false, getTag, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		owner, repo, err := ownerRepo(req)
		if err != nil {
			return errResult("get tag", err)
		}
		tag, err := req.RequireString("tag")
		if err != nil {
			return errResult("get tag", err)
		}
		ref, _, err := client.Git.GetRef(ctx, owner, repo, "tags/"+tag)
		if err != nil {
			return errResult("get tag", err)
		}
		if ref.Object == nil || ref.Object.GetSHA() == "" {
			return errResult("get tag", fmt.Errorf("ref %q has no target object", "tags/"+tag))
		}
		if ref.Object.GetType() != "tag" {
			return jsonResult(map[string]any{
				"tag":        tag,
				"type":       "lightweight",
				"commit_sha": ref.Object.GetSHA(),
				"note":       "lightweight tag points straight at a commit; use get_commit for details",
			})
		}
		tagObj, _, err := client.Git.GetTag(ctx, owner, repo, ref.Object.GetSHA())
		if err != nil {
			return errResult("get tag", err)
		}
		return jsonResult(tagObj)
	})

	listCollaborators := mcp.NewTool("list_repository_collaborators",
		mcp.WithDescription("List collaborators of a repository."),
		withRepoArgs(),
		mcp.WithString("affiliation", mcp.Description("Filter by affiliation, passed through to the GitHub API (e.g. all, outside)")),
		mcp.WithString("permission", mcp.Description("Filter by permission: pull, triage, push, maintain, admin")),
		withPagination(),
	)
	r.Add("repos", false, listCollaborators, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		owner, repo, err := ownerRepo(req)
		if err != nil {
			return errResult("list repository collaborators", err)
		}
		opts := &github.ListCollaboratorsOptions{
			Affiliation: req.GetString("affiliation", ""),
			Permission:  req.GetString("permission", ""),
			ListOptions: *listOptions(req),
		}
		users, resp, err := client.Repositories.ListCollaborators(ctx, owner, repo, opts)
		if err != nil {
			return errResult("list repository collaborators", err)
		}
		return jsonResultWithPage(resp, users)
	})

	createRepository := mcp.NewTool("create_repository",
		mcp.WithDescription("Create a repository under the authenticated user, or under org when set. "+
			"Consider auto_init=true so the repository starts with a README."),
		mcp.WithString("name", mcp.Required(), mcp.Description("Repository name")),
		mcp.WithString("description", mcp.Description("Repository description")),
		mcp.WithBoolean("private", mcp.Description("Create as private (default false)")),
		mcp.WithBoolean("auto_init", mcp.Description("Initialize the repository with an empty README (default false)")),
		mcp.WithString("org", mcp.Description("Organization login; omit to create under the authenticated user")),
	)
	r.Add("repos", true, createRepository, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		name, err := req.RequireString("name")
		if err != nil {
			return errResult("create repository", err)
		}
		newRepo := &github.Repository{
			Name:     github.Ptr(name),
			Private:  github.Ptr(req.GetBool("private", false)),
			AutoInit: github.Ptr(req.GetBool("auto_init", false)),
		}
		if v := req.GetString("description", ""); v != "" {
			newRepo.Description = github.Ptr(v)
		}
		repository, _, err := client.Repositories.Create(ctx, req.GetString("org", ""), newRepo)
		if err != nil {
			return errResult("create repository", err)
		}
		return jsonResult(repository)
	})

	deleteRepository := mcp.NewTool("delete_repository",
		mcp.WithDescription("Permanently delete a repository. Destructive and irreversible: "+
			"pass confirm with the exact 'owner/repo' name to proceed."),
		withRepoArgs(),
		mcp.WithString("confirm", mcp.Required(), mcp.Description("Must be exactly 'owner/repo' to confirm deletion")),
	)
	r.Add("repos", true, deleteRepository, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		owner, repo, err := ownerRepo(req)
		if err != nil {
			return errResult("delete repository", err)
		}
		confirm, err := req.RequireString("confirm")
		if err != nil {
			return errResult("delete repository", err)
		}
		if confirm != owner+"/"+repo {
			return errResult("delete repository", fmt.Errorf(
				"confirm %q does not match %q; type the full repository name to confirm deletion", confirm, owner+"/"+repo))
		}
		if _, err := client.Repositories.Delete(ctx, owner, repo); err != nil {
			return errResult("delete repository", err)
		}
		return jsonResult(map[string]any{"deleted": owner + "/" + repo})
	})

	forkRepository := mcp.NewTool("fork_repository",
		mcp.WithDescription("Fork a repository to the authenticated user or to org. "+
			"Forking is asynchronous: a 202 response means the fork is still being created."),
		withRepoArgs(),
		mcp.WithString("org", mcp.Description("Organization login to fork into; omit to fork to the authenticated user")),
		mcp.WithString("name", mcp.Description("Optional new name for the forked repository")),
	)
	r.Add("repos", true, forkRepository, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		owner, repo, err := ownerRepo(req)
		if err != nil {
			return errResult("fork repository", err)
		}
		opts := &github.RepositoryCreateForkOptions{}
		if v := req.GetString("org", ""); v != "" {
			opts.Organization = v
		}
		if v := req.GetString("name", ""); v != "" {
			opts.Name = v
		}
		repository, _, err := client.Repositories.CreateFork(ctx, owner, repo, opts)
		if err != nil {
			// GitHub reports async fork creation as 202 + AcceptedError; the
			// fork is being created and the caller should poll get_repository.
			var aerr *github.AcceptedError
			if errors.As(err, &aerr) {
				return jsonResult(map[string]any{
					"status": "fork_in_progress",
					"note":   "the fork is being created asynchronously; check back with get_repository",
				})
			}
			return errResult("fork repository", err)
		}
		return jsonResult(repository)
	})
}
