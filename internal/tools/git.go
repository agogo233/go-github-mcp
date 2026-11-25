package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/google/go-github/v72/github"
	mcp "github.com/mark3labs/mcp-go/mcp"
)

// maxBlobBytes caps how large a blob get_blob will return. The tool ships
// blob content through the agent's context, so unbounded sizes would blow up
// both the process and the conversation; 1 MiB covers source files amply.
const maxBlobBytes = 1 << 20

// RegisterGit registers the "git" toolset: low-level Git data API operations
// including API-based multi-file commits (push_files), which remove the need
// for any git credentials inside the container.
func RegisterGit(r Registrar, client *github.Client) {
	listBranches := mcp.NewTool("list_branches",
		mcp.WithDescription("List branches of a repository."),
		withRepoArgs(),
		withPagination(),
	)
	r.Add("git", false, listBranches, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		owner, repo, err := ownerRepo(req)
		if err != nil {
			return errResult("list branches", err)
		}
		branches, resp, err := client.Repositories.ListBranches(ctx, owner, repo, &github.BranchListOptions{
			ListOptions: *listOptions(req),
		})
		if err != nil {
			return errResult("list branches", err)
		}
		return jsonResultWithPage(resp, branches)
	})

	createBranch := mcp.NewTool("create_branch",
		mcp.WithDescription("Create a new branch from an existing branch (or the default branch)."),
		withRepoArgs(),
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
		withRepoArgs(),
		mcp.WithString("sha", mcp.Description("Branch, tag or commit SHA to start from")),
		mcp.WithString("path", mcp.Description("Only commits touching this file path")),
		withPagination(),
	)
	r.Add("git", false, listCommits, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		owner, repo, err := ownerRepo(req)
		if err != nil {
			return errResult("list commits", err)
		}
		opts := &github.CommitsListOptions{ListOptions: *listOptions(req)}
		opts.SHA = req.GetString("sha", "")
		opts.Path = req.GetString("path", "")
		commits, resp, err := client.Repositories.ListCommits(ctx, owner, repo, opts)
		if err != nil {
			return errResult("list commits", err)
		}
		return jsonResultWithPage(resp, commits)
	})

	getCommit := mcp.NewTool("get_commit",
		mcp.WithDescription("Get one commit with its diff stats and files."),
		withRepoArgs(),
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

	compareCommits := mcp.NewTool("compare_commits",
		mcp.WithDescription("Compare two commits, branches or tags (base...head): returns status, "+
			"ahead_by, behind_by, total_commits, the commits unique to head and the changed files. "+
			"When comparing a fork against upstream (base=upstream-owner:main, head=your-owner:branch on the fork), "+
			"behind_by counts how many commits upstream is ahead and ahead_by/commits are the local-only commits. "+
			"The owner prefix in base/head must be the owner login name, never the repo name "+
			"(base=zeroclaw-labs:master, not zeroclaw:master). "+
			"For wide divergences set meta_only=true (counts only) or per_page=1 to keep the payload small. "+
			"format=diff or patch returns the raw diff/patch text instead of JSON."),
		withRepoArgs(),
		mcp.WithString("base", mcp.Required(), mcp.Description("Base ref: branch, tag or SHA (cross-fork format owner:branch)")),
		mcp.WithString("head", mcp.Required(), mcp.Description("Head ref: branch, tag or SHA (cross-fork format owner:branch)")),
		mcp.WithString("format", mcp.Description("Response format: json (default), diff or patch")),
		mcp.WithBoolean("meta_only", mcp.Description("Return only status, ahead_by, behind_by, total_commits and merge_base_sha, "+
			"dropping commits and files so wide divergences stay far below the payload cap")),
		withPagination(),
	)
	r.Add("git", false, compareCommits, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		owner, repo, err := ownerRepo(req)
		if err != nil {
			return errResult("compare commits", err)
		}
		base, err := req.RequireString("base")
		if err != nil {
			return errResult("compare commits", err)
		}
		head, err := req.RequireString("head")
		if err != nil {
			return errResult("compare commits", err)
		}
		switch format := req.GetString("format", "json"); format {
		case "json":
			comp, resp, err := client.Repositories.CompareCommits(ctx, owner, repo, base, head, listOptions(req))
			if err != nil {
				if resp != nil && resp.StatusCode == http.StatusNotFound {
					// A 404 here is almost always a malformed cross-fork ref:
					// the owner prefix must be the owner login name, not the
					// repo name (zeroclaw-labs:master, not zeroclaw:master).
					if i := strings.LastIndex(base, ":"); i >= 0 {
						return mcp.NewToolResultError(fmt.Sprintf(
							"failed to compare commits: 404 not found. For cross-fork compares the owner prefix "+
								"must be the owner login name, not the repository name (use %q, not %q); "+
								"also check that both refs exist and are accessible with the current token",
							owner+":"+base[i+1:], base)), nil
					}
					return mcp.NewToolResultError(fmt.Sprintf(
						"failed to compare commits: 404 not found. If this is a cross-fork compare, prefix each ref "+
							"with its owner login name (e.g. %q); also check that both refs exist and are accessible "+
							"with the current token",
						owner+":"+base)), nil
				}
				return errResult("compare commits", err)
			}
			return compareJSONResult(resp, trimComparison(comp, req.GetBool("meta_only", false)))
		case "diff", "patch":
			rawType := github.Diff
			if format == "patch" {
				rawType = github.Patch
			}
			raw, _, err := client.Repositories.CompareCommitsRaw(ctx, owner, repo, base, head, github.RawOptions{Type: rawType})
			if err != nil {
				return errResult("compare commits", err)
			}
			if len(raw) > maxCompareBytes {
				return mcp.NewToolResultError(fmt.Sprintf(
					"compare %s is %d bytes, above the %d MiB limit; "+
						"compare a narrower base...head range or fetch single commits with get_commit instead",
					format, len(raw), maxCompareBytes>>20)), nil
			}
			return mcp.NewToolResultText(raw), nil
		default:
			return mcp.NewToolResultError(fmt.Sprintf("invalid format %q; use json, diff or patch", format)), nil
		}
	})

	getBlob := mcp.NewTool("get_blob",
		mcp.WithDescription("Get the raw content of a git blob by SHA "+
			"(capped at 1 MiB; larger blobs are rejected with their size)."),
		withRepoArgs(),
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
		if size := blob.GetSize(); size > maxBlobBytes {
			return mcp.NewToolResultError(fmt.Sprintf(
				"blob %s is %d bytes, above the %d MiB get_blob limit; "+
					"locate the file with get_tree and fetch it through a raw download instead",
				sha, size, maxBlobBytes>>20)), nil
		}
		return jsonResult(blob)
	})

	getTree := mcp.NewTool("get_tree",
		mcp.WithDescription("Get a git tree (directory structure) by SHA."),
		withRepoArgs(),
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
		withRepoArgs(),
		mcp.WithString("branch", mcp.Required(), mcp.Description("Target branch; must already exist (create it with create_branch first)")),
		mcp.WithString("message", mcp.Required(), mcp.Description("Commit message")),
		mcp.WithArray("files", mcp.Required(),
			mcp.Description("Array of {\"path\": \"file/path.txt\", \"content\": \"full text\"} objects; "+
				"each path may appear once; optional per-file \"mode\": 100644 (default), 100755 (executable) or 120000 (symlink)"),
			mcp.Items(map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path":    map[string]any{"type": "string"},
					"content": map[string]any{"type": "string"},
					"mode":    map[string]any{"type": "string", "enum": []string{"100644", "100755", "120000"}},
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
	Mode    string
}

// allowedBlobModes are the git file modes push_files accepts: regular,
// executable, and symlink entries. Anything else (submodule, tree) is out of
// scope for a text-content commit tool.
var allowedBlobModes = map[string]bool{"100644": true, "100755": true, "120000": true}

// parseFileEntries extracts the files array from the tool arguments,
// defaulting the mode and rejecting duplicate paths with a clear message
// instead of relying on the API's late, less specific failure.
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
	seen := map[string]bool{}
	for i, item := range items {
		obj, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("files[%d] must be an object with path and content", i)
		}
		path, _ := obj["path"].(string)
		content, _ := obj["content"].(string)
		mode, _ := obj["mode"].(string)
		if path == "" {
			return nil, fmt.Errorf("files[%d].path is required", i)
		}
		if mode == "" {
			mode = "100644"
		}
		if !allowedBlobModes[mode] {
			return nil, fmt.Errorf("files[%d].mode %q is invalid (use 100644, 100755 or 120000)", i, mode)
		}
		if seen[path] {
			return nil, fmt.Errorf("files[%d].path %q appears more than once; each path may be listed once", i, path)
		}
		seen[path] = true
		out = append(out, fileEntry{Path: path, Content: content, Mode: mode})
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
			Mode: github.Ptr(f.Mode),
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

// maxCompareBytes caps the compare_commits payload. A diverged compare can
// carry hundreds of commits each embedding file patches, which would flood
// the agent context; oversized results are rejected with remediation hints
// (same policy as get_blob).
const maxCompareBytes = 1 << 20

// compareResult is the trimmed compare_commits payload. The full base and
// merge-base commit objects are dropped: base echoes an input and both
// duplicate patches already available via get_commit; only the merge-base
// SHA is kept to locate the fork point.
type compareResult struct {
	Status       *string                    `json:"status,omitempty"`
	AheadBy      *int                       `json:"ahead_by,omitempty"`
	BehindBy     *int                       `json:"behind_by,omitempty"`
	TotalCommits *int                       `json:"total_commits,omitempty"`
	MergeBaseSHA string                     `json:"merge_base_sha,omitempty"`
	Commits      []*github.RepositoryCommit `json:"commits,omitempty"`
	Files        []*github.CommitFile       `json:"files,omitempty"`
	HTMLURL      *string                    `json:"html_url,omitempty"`
	PermalinkURL *string                    `json:"permalink_url,omitempty"`
	DiffURL      *string                    `json:"diff_url,omitempty"`
	PatchURL     *string                    `json:"patch_url,omitempty"`
}

// trimComparison projects the API comparison onto compareResult. With
// metaOnly the per-commit and per-file payloads are dropped: callers asking
// only for the ahead/behind counters get a payload that stays far below the
// response cap even for wide, long-diverged branches.
func trimComparison(comp *github.CommitsComparison, metaOnly bool) *compareResult {
	if comp == nil {
		return nil
	}
	res := &compareResult{
		Status:       comp.Status,
		AheadBy:      comp.AheadBy,
		BehindBy:     comp.BehindBy,
		TotalCommits: comp.TotalCommits,
		MergeBaseSHA: comp.MergeBaseCommit.GetSHA(),
		HTMLURL:      comp.HTMLURL,
		PermalinkURL: comp.PermalinkURL,
		DiffURL:      comp.DiffURL,
		PatchURL:     comp.PatchURL,
	}
	if !metaOnly {
		res.Commits = comp.Commits
		res.Files = comp.Files
	}
	return res
}

// compareJSONResult marshals the trimmed comparison with the same result
// shape as jsonResultWithPage, enforcing the maxCompareBytes cap.
func compareJSONResult(resp *github.Response, v any) (*mcp.CallToolResult, error) {
	data, err := json.Marshal(normalizeNilSlice(v))
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to encode result: %v", err)), nil
	}
	if len(data) > maxCompareBytes {
		return mcp.NewToolResultError(fmt.Sprintf(
			"compare result is %d bytes, above the %d MiB limit; "+
				"reduce per_page, compare a narrower base...head range, or use format=diff",
			len(data), maxCompareBytes>>20)), nil
	}
	res := &mcp.CallToolResult{Content: []mcp.Content{mcp.TextContent{Type: "text", Text: string(data)}}}
	if resp != nil {
		meta := map[string]any{"has_next": resp.NextPage != 0}
		if resp.NextPage != 0 {
			meta["next_page"] = resp.NextPage
		}
		if encoded, err := json.Marshal(meta); err == nil {
			res.Content = append(res.Content, mcp.TextContent{Type: "text", Text: string(encoded)})
		}
	}
	return res, nil
}
