package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/go-github/v72/github"
	mcp "github.com/mark3labs/mcp-go/mcp"
)

// Projects v2 is a GraphQL-only API; go-github has no typed service for it.
// These tools POST hand-crafted GraphQL queries through the shared client.
// Note: the GraphQL endpoint path differs on GHES, so these tools target
// github.com (and ghe.com data-residency hosts) in practice.
//
// graphqlQuery unwraps the standard GraphQL envelope: transport-level
// failures surface as errors from client.Do, while GraphQL-level failures
// arrive as HTTP 200 with a non-empty "errors" array and would otherwise be
// reported as successful tool calls. Any errors — including partial ones
// returned alongside data — are converted into a Go error so handlers can
// emit a failed tool result. On success the "data" payload is decoded into
// out (callers no longer see the envelope wrapper).
func graphqlQuery(ctx context.Context, client *github.Client, query string, variables map[string]any, out any) error {
	req, err := client.NewRequest("POST", "graphql", map[string]any{
		"query":     query,
		"variables": variables,
	})
	if err != nil {
		return err
	}
	var envelope struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if _, err := client.Do(ctx, req, &envelope); err != nil {
		return err
	}
	if len(envelope.Errors) > 0 {
		msgs := make([]string, 0, len(envelope.Errors))
		for _, e := range envelope.Errors {
			if e.Message != "" {
				msgs = append(msgs, e.Message)
			}
		}
		if len(msgs) == 0 {
			return errors.New("graphql request failed")
		}
		return fmt.Errorf("graphql error: %s", strings.Join(msgs, "; "))
	}
	if out == nil || len(envelope.Data) == 0 {
		return nil
	}
	return json.Unmarshal(envelope.Data, out)
}

const listProjectsQuery = `
query($owner:String!, $repo:String!) {
  repository(owner:$owner, name:$repo) {
    projectsV2(first: 50) {
      pageInfo { hasNextPage }
      nodes { id title number url closed }
    }
  }
}`

const listProjectItemsQuery = `
query($owner:String!, $repo:String!, $number:Int!, $first:Int!) {
  repository(owner:$owner, name:$repo) {
    projectV2(number:$number) {
      title
      items(first:$first) {
        totalCount
        nodes {
          id
          type
          fieldValues(first: 20) {
            nodes {
              ... on ProjectV2ItemFieldSingleSelectValue { name }
              ... on ProjectV2ItemFieldTextValue { text }
              ... on ProjectV2ItemFieldDateValue { date }
            }
          }
          content {
            __typename
            ... on Issue { number title state url }
            ... on PullRequest { number title state url }
            ... on DraftIssue { title }
          }
        }
      }
    }
  }
}`

// RegisterProjects registers the "projects" toolset (Projects v2, GraphQL).
func RegisterProjects(r Registrar, client *github.Client) {
	listProjects := mcp.NewTool("list_projects",
		mcp.WithDescription("List Projects v2 of a repository (GitHub.com; Projects v2 uses the GraphQL API). "+
			"Returns at most the first 50 projects; pageInfo.hasNextPage in the result signals more exist."),
		withRepoArgs(),
	)
	r.Add("projects", false, listProjects, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		owner, repo, err := ownerRepo(req)
		if err != nil {
			return errResult("list projects", err)
		}
		var out any
		err = graphqlQuery(ctx, client, listProjectsQuery,
			map[string]any{"owner": owner, "repo": repo}, &out)
		if err != nil {
			return errResult("list projects", err)
		}
		return jsonResult(out)
	})

	listItems := mcp.NewTool("list_project_items",
		mcp.WithDescription("List items of a Projects v2 board with their field values (status, etc.)."),
		withRepoArgs(),
		mcp.WithNumber("project_number", mcp.Required(), mcp.Description("Project v2 number (not ID)")),
		mcp.WithNumber("first", mcp.Description("Max items to return (default 20, max 100)")),
	)
	r.Add("projects", false, listItems, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		owner, repo, err := ownerRepo(req)
		if err != nil {
			return errResult("list project items", err)
		}
		number, res := intIDArg(req, "project_number", "list project items")
		if res != nil {
			return res, nil
		}
		first := req.GetInt("first", 20)
		if first < 1 {
			first = 20
		}
		if first > 100 {
			first = 100
		}
		var out any
		err = graphqlQuery(ctx, client, listProjectItemsQuery, map[string]any{
			"owner": owner, "repo": repo, "number": number, "first": first,
		}, &out)
		if err != nil {
			return errResult("list project items (note: Projects v2 requires GraphQL access and github.com)", err)
		}
		return jsonResult(out)
	})
}
