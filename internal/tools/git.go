package tools

import (
	"context"
	"fmt"

	"github.com/google/go-github/v72/github"
	mcp "github.com/mark3labs/mcp-go/mcp"
)

// RegisterGit registers the "git" toolset: low-level Git data API operations
// including API-based multi-file commits (push_files), which remove the need
// for any git credentials inside the container.
func RegisterGit(r Registrar, client *github.Client) {
	listBranches := mcp.NewTool("list_branches",
		mcp.WithDescription("List branches of a repository."),
		mcp.WithString("owner", mcp.Required(), mcp.Description("Repository owner")),
		mcp.WithString("repo", mcp.Required(), mcp.Description("Repository name")),
		mcp.WithNumber("page", mcp.Description("Page number (starts at 1)")),
		mcp.WithNumber("per_page", mcp.Description("Results per page (default 30, max 100)")),
	)
	r.Add("git", false, listBranches, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		owner, repo, err := ownerRepo(req)
		if err != nil {
			return errResult("list branches", err)
		}
		branches, _, err := client.Repositories.ListBranches(ctx, owner, repo, &github.BranchListOptions{
			ListOptions: *listOptions(req),
		})
		if err != nil {
			return errResult("list branches", err)
		}
		return jsonResult(branches)
	})

	createBranch := mcp.NewTool("create_branch",
		mcp.WithDescription("Create a new branch from an existing branch (or the default branch)."),
		mcp.WithString("owner", mcp.Required(), mcp.Description("Repository owner")),
		mcp.WithString("repo", mcp.Required(), mcp.Description("Repository name")),
		mcp.WithString("branch", mcp.Required(), mcp.Description("Name of the new branch")),
		mcp.WithString("from_branch", mcp.Description("Source branch; defaults to the repository default branch")),
	)
	r.Add("git", true, createBranch, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		owner, repo, err := ownerRepo(req)
		if err != nil {
			return errResult("create branch", err)
		}
		branch, err := req.RequireString("branch")
		if err != nil {
			return errResult("create branch", err)
		}
		from := req.GetString("from_branch", "")
		if from == "" {
			repository, _, err := client.Repositories.Get(ctx, owner, repo)
			if err != nil {
				return errResult("resolve default branch", err)
			}
			from = repository.GetDefaultBranch()
			if from == "" {
				from = "main"
			}
		}
		base, _, err := client.Git.GetRef(ctx, owner, repo, "heads/"+from)
		if err != nil {
			return errResult(fmt.Sprintf("resolve source branch %s", from), err)
		}
		baseSHA := base.GetObject().GetSHA()
		if baseSHA == "" {
			return mcp.NewToolResultError(fmt.Sprintf("source branch %q has no head commit", from)), nil
		}
		ref, _, err := client.Git.CreateRef(ctx, owner, repo, &github.Reference{
			Ref:    github.Ptr("refs/heads/" + branch),
			Object: &github.GitObject{Type: github.Ptr("commit"), SHA: github.Ptr(baseSHA)},
		})
		if err != nil {
			return errResult("create branch", err)
		}
		return jsonResult(ref)
	})

	listCommits := mcp.NewTool("list_commits",
		mcp.WithDescription("List commits of a repository, optionally starting from a branch/tag/SHA."),
		mcp.WithString("owner", mcp.Required(), mcp.Description("Repository owner")),
		mcp.WithString("repo", mcp.Required(), mcp.Description("Repository name")),
		mcp.WithString("sha", mcp.Description("Branch, tag or commit SHA to start from")),
		mcp.WithString("path", mcp.Description("Only commits touching this file path")),
		mcp.WithNumber("page", mcp.Description("Page number (starts at 1)")),
		mcp.WithNumber("per_page", mcp.Description("Results per page (default 30, max 100)")),
	)
	r.Add("git", false, listCommits, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		owner, repo, err := ownerRepo(req)
		if err != nil {
			return errResult("list commits", err)
		}
		opts := &github.CommitsListOptions{ListOptions: *listOptions(req)}
		opts.SHA = req.GetString("sha", "")
		opts.Path = req.GetString("path", "")
		commits, _, err := client.Repositories.ListCommits(ctx, owner, repo, opts)
		if err != nil {
			return errResult("list commits", err)
		}
		return jsonResult(commits)
	})

	getCommit := mcp.NewTool("get_commit",
		mcp.WithDescription("Get one commit with its diff stats and files."),
		mcp.WithString("owner", mcp.Required(), mcp.Description("Repository owner")),
		mcp.WithString("repo", mcp.Required(), mcp.Description("Repository name")),
		mcp.WithString("sha", mcp.Required(), mcp.Description("Commit SHA (or branch/tag ref)")),
	)
	r.Add("git", false, getCommit, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		owner, repo, err := ownerRepo(req)
		if err != nil {
			return errResult("get commit", err)
		}
		sha, err := req.RequireString("sha")
		if err != nil {
			return errResult("get commit", err)
		}
		commit, _, err := client.Repositories.GetCommit(ctx, owner, repo, sha, nil)
		if err != nil {
			return errResult("get commit", err)
		}
		return jsonResult(commit)
	})

	getBlob := mcp.NewTool("get_blob",
		mcp.WithDescription("Get the raw content of a git blob by SHA (works for files of any size)."),
		mcp.WithString("owner", mcp.Required(), mcp.Description("Repository owner")),
		mcp.WithString("repo", mcp.Required(), mcp.Description("Repository name")),
		mcp.WithString("sha", mcp.Required(), mcp.Description("Blob SHA")),
	)
	r.Add("git", false, getBlob, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		owner, repo, err := ownerRepo(req)
		if err != nil {
			return errResult("get blob", err)
		}
		sha, err := req.RequireString("sha")
		if err != nil {
			return errResult("get blob", err)
		}
		blob, _, err := client.Git.GetBlob(ctx, owner, repo, sha)
		if err != nil {
			return errResult("get blob", err)
		}
		return jsonResult(blob)
	})

	getTree := mcp.NewTool("get_tree",
		mcp.WithDescription("Get a git tree (directory structure) by SHA."),
		mcp.WithString("owner", mcp.Required(), mcp.Description("Repository owner")),
		mcp.WithString("repo", mcp.Required(), mcp.Description("Repository name")),
		mcp.WithString("sha", mcp.Required(), mcp.Description("Tree SHA (a commit SHA resolves to its root tree)")),
		mcp.WithBoolean("recursive", mcp.Description("Include nested trees recursively")),
	)
	r.Add("git", false, getTree, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		owner, repo, err := ownerRepo(req)
		if err != nil {
			return errResult("get tree", err)
		}
		sha, err := req.RequireString("sha")
		if err != nil {
			return errResult("get tree", err)
		}
		tree, _, err := client.Git.GetTree(ctx, owner, repo, sha, req.GetBool("recursive", false))
		if err != nil {
			return errResult("get tree", err)
		}
		return jsonResult(tree)
	})

	pushFiles := mcp.NewTool("push_files",
		mcp.WithDescription("Commit multiple text files to a branch in a single commit via the Git data API "+
			"(equivalent of editing files then running git commit + git push, without needing git credentials)."),
		mcp.WithString("owner", mcp.Required(), mcp.Description("Repository owner")),
		mcp.WithString("repo", mcp.Required(), mcp.Description("Repository name")),
		mcp.WithString("branch", mcp.Required(), mcp.Description("Target branch; must already exist (create it with create_branch first)")),
		mcp.WithString("message", mcp.Required(), mcp.Description("Commit message")),
		mcp.WithArray("files", mcp.Required(),
			mcp.Description("Array of {\"path\": \"file/path.txt\", \"content\": \"full text\"} objects"),
			mcp.Items(map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path":    map[string]any{"type": "string"},
					"content": map[string]any{"type": "string"},
				},
				"required": []string{"path", "content"},
			}),
		),
	)
	r.Add("git", true, pushFiles, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		owner, repo, err := ownerRepo(req)
		if err != nil {
			return errResult("push files", err)
		}
		branch, err := req.RequireString("branch")
		if err != nil {
			return errResult("push files", err)
		}
		message, err := req.RequireString("message")
		if err != nil {
			return errResult("push files", err)
		}
		files, err := parseFileEntries(req)
		if err != nil {
			return errResult("push files", err)
		}
		result, err := pushFilesCommit(ctx, client, owner, repo, branch, message, files)
		if err != nil {
			return errResult("push files", err)
		}
		return jsonResult(result)
	})
}

// fileEntry is one file to commit via push_files.
type fileEntry struct {
	Path    string
	Content string
}

// parseFileEntries extracts the files array from the tool arguments.
func parseFileEntries(req mcp.CallToolRequest) ([]fileEntry, error) {
	raw, ok := req.GetArguments()["files"]
	if !ok {
		return nil, fmt.Errorf("files is required")
	}
	items, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("files must be an array of {path, content} objects")
	}
	var out []fileEntry
	for i, item := range items {
		obj, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("files[%d] must be an object with path and content", i)
		}
		path, _ := obj["path"].(string)
		content, _ := obj["content"].(string)
		if path == "" {
			return nil, fmt.Errorf("files[%d].path is required", i)
		}
		out = append(out, fileEntry{Path: path, Content: content})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("files must contain at least one entry")
	}
	return out, nil
}

// pushFilesCommit builds one commit on top of the branch head containing all
// given files, then fast-forwards the branch ref to it.
func pushFilesCommit(ctx context.Context, client *github.Client, owner, repo, branch, message string, files []fileEntry) (*github.Commit, error) {
	baseRef, _, err := client.Git.GetRef(ctx, owner, repo, "heads/"+branch)
	if err != nil {
		return nil, fmt.Errorf("resolve branch %q (create it first with create_branch): %w", branch, err)
	}
	baseSHA := baseRef.GetObject().GetSHA()
	if baseSHA == "" {
		return nil, fmt.Errorf("branch %q has no head commit", branch)
	}
	baseCommit, _, err := client.Repositories.GetCommit(ctx, owner, repo, baseSHA, nil)
	if err != nil {
		return nil, fmt.Errorf("resolve base commit %s: %w", baseSHA, err)
	}
	baseTree := baseCommit.GetCommit().GetTree().GetSHA()

	var entries []*github.TreeEntry
	for _, f := range files {
		blob, _, err := client.Git.CreateBlob(ctx, owner, repo, &github.Blob{
			Content:  github.Ptr(f.Content),
			Encoding: github.Ptr("utf-8"),
		})
		if err != nil {
			return nil, fmt.Errorf("create blob for %s: %w", f.Path, err)
		}
		entries = append(entries, &github.TreeEntry{
			Path: github.Ptr(f.Path),
			Mode: github.Ptr("100644"),
			Type: github.Ptr("blob"),
			SHA:  blob.SHA,
		})
	}

	tree, _, err := client.Git.CreateTree(ctx, owner, repo, baseTree, entries)
	if err != nil {
		return nil, fmt.Errorf("create tree: %w", err)
	}

	commit, _, err := client.Git.CreateCommit(ctx, owner, repo, &github.Commit{
		Message: github.Ptr(message),
		Tree:    &github.Tree{SHA: tree.SHA},
		Parents: []*github.Commit{{SHA: github.Ptr(baseSHA)}},
	}, nil)
	if err != nil {
		return nil, fmt.Errorf("create commit: %w", err)
	}

	_, _, err = client.Git.UpdateRef(ctx, owner, repo, &github.Reference{
		Ref:    github.Ptr("refs/heads/" + branch),
		Object: &github.GitObject{Type: github.Ptr("commit"), SHA: commit.SHA},
	}, false)
	if err != nil {
		return nil, fmt.Errorf("update ref (non-fast-forward rejected): %w", err)
	}
	return commit, nil
}
