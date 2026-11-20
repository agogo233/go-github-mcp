# go-github-mcp

[![CI](https://github.com/agogo233/go-github-mcp/actions/workflows/ci.yml/badge.svg)](https://github.com/agogo233/go-github-mcp/actions/workflows/ci.yml)
[![Docker](https://github.com/agogo233/go-github-mcp/actions/workflows/docker.yml/badge.svg)](https://github.com/agogo233/go-github-mcp/actions/workflows/docker.yml)

A multi-identity **GitHub MCP Server** written in Go, shipped as a Docker image.
It lets MCP clients (Claude, VS Code, Cursor, OpenCode, custom agents, ...)
operate on GitHub through the REST/GraphQL APIs while the **personal access
tokens (PATs) never leave the server container**.

> Status: M4 — `stdio` + Streamable `HTTP` transports, config hardening,
> toolset/read-only policy framework, and 100 tools across 18 toolsets
> (context, users, repos, git, issues, labels, pull_requests, actions,
> notifications, gists, discussions, stargazers, orgs, code_security,
> secret_protection, dependabot, security_advisories, projects).

## Why

- **Multi-identity service**: deploy once on an internal server; each client
  authenticates with its own identity key and acts as the GitHub account bound
  to it, while all PATs live only inside the container.
- **Auditable**: every HTTP request is logged with the self-reported
  `X-Client-Name` header (operational insight, not strong attribution), the
  invoked tool name (HTTP-request level, without tool arguments), and the
  resolved `gh_login` GitHub account.
- **Least privilege**: toolsets can be trimmed via `GITHUB_TOOLSETS`, write
  tools can be globally disabled with `READ_ONLY=true`.

## Security model

| Layer | Control |
|---|---|
| Token storage | PATs injected via environment/secret only; never written to disk, never logged |
| Client side | MCP clients only know the server URL + their identity key; they never see any PAT |
| Transport auth | `Authorization: Bearer <GITHUB_MCP_AUTH_TOKEN key>` enforced on `/mcp` only (`/healthz` is unauthenticated); constant-time compare, one identity per key |
| Startup guard | Every configured PAT self-checked against `/user`; HTTP mode refuses to start without `GITHUB_MCP_AUTH_TOKEN` |
| Blast radius | `GITHUB_TOOLSETS` / `GITHUB_TOOLS` allow-list, `READ_ONLY` kill switch for all write tools |
| GHES | `GITHUB_HOST` must be https (loopback http tolerated for local dev) |

If a PAT is ever compromised: revoke it on GitHub, rotate the matching
`GITHUB_MCP_AUTH_TOKEN` entry (or `GITHUB_PERSONAL_ACCESS_TOKEN`), restart the
container, and review audit logs.

## Configuration

| Variable | Required | Default | Description |
|---|---|---|---|
| `GITHUB_PERSONAL_ACCESS_TOKEN` | yes | – | GitHub PAT used for stdio, the `ALLOW_UNAUTHENTICATED` fallback, and startup self-check (fine-grained recommended) |
| `GITHUB_MCP_AUTH_TOKEN` | http mode | – | Identity map: comma-separated `key:pat` pairs. Each `key` is a Bearer secret a client presents; the bound `pat` is the GitHub identity it acts as. Use strong random keys, e.g. `openssl rand -hex 32` |
| `MCP_TRANSPORT` | no | `stdio` | `stdio` or `http` |
| `HTTP_ADDR` | no | `0.0.0.0:8080` | HTTP listen address (HTTP mode); the image `HEALTHCHECK` always probes port 8080, so keep it when changing the address |
| `ALLOW_UNAUTHENTICATED` | no | `false` | Local-debugging escape hatch to skip Bearer auth (falls back to `GITHUB_PERSONAL_ACCESS_TOKEN`); ignored when `GITHUB_MCP_AUTH_TOKEN` is set, since configured identities take precedence |
| `GITHUB_HOST` | no | `github.com` | GHES / ghe.com hostname (https enforced) |
| `GITHUB_TOOLSETS` | no | `default` | Comma list: `default`, `all`, or named toolsets |
| `GITHUB_TOOLS` | no | – | Comma list of individual tool names to **keep**; intersected with the toolsets enabled via `GITHUB_TOOLSETS` (a listed tool whose toolset is not enabled is skipped with a startup warning) |
| `READ_ONLY` | no | `false` | Skip registration of all write tools |
| `LOG_LEVEL` | no | `info` | Log verbosity: `debug`, `info`, or `warn`; `debug` also prints one line per skipped tool at startup |
| `HTTP_RATE_LIMIT` | no | `10` | Sustained `/mcp` requests per second in HTTP mode; `0` (or negative) disables rate limiting |
| `HTTP_RATE_BURST` | no | `20` | Burst allowance on top of the sustained rate |
| `HTTP_MAX_CONCURRENT` | no | `16` | Max in-flight `/mcp` POST requests; `0` (or negative) disables the cap. Long-lived GET notification streams are not counted |

## Agent onboarding

Agents get usage guidance for free: the server returns `instructions` in the
MCP initialize response, covering the auth check via `get_me`, the
`owner`/`repo` parameter convention, `page`/`per_page` pagination, JSON-in-text
results, error handling, and the current read-only mode. No extra setup is
needed on the agent side; this README remains the reference for human
operators.

## Run with Docker (team-shared HTTP mode)

```bash
cat > .env <<'EOF'
GITHUB_PERSONAL_ACCESS_TOKEN=github_pat_xxx
# key:pat pairs; one identity per key. Use strong random keys,
# e.g. openssl rand -hex 32
GITHUB_MCP_AUTH_TOKEN=agent1:github_pat_xxx,agent2:github_pat_yyy
EOF

docker compose up -d
curl http://127.0.0.1:8080/healthz   # -> ok
```

MCP endpoint: `http://<host>:8080/mcp` (Streamable HTTP).
For LAN/team use, terminate TLS with a reverse proxy (nginx/Caddy) in front.
Clients authenticate with one `key` from `GITHUB_MCP_AUTH_TOKEN` as their
Bearer token; the server routes that identity to its bound PAT.

## Run multiple identities

One container can back several GitHub accounts: each `key:pat` pair in
`GITHUB_MCP_AUTH_TOKEN` is one identity. To operate your personal account plus
a bot in a single deployment:

```bash
cat > .env <<'EOF'
GITHUB_PERSONAL_ACCESS_TOKEN=github_pat_main_xxx
GITHUB_MCP_AUTH_TOKEN=personal:github_pat_main_xxx,bot:github_pat_bot_yyy
EOF

docker compose up -d
curl http://127.0.0.1:8080/healthz   # -> ok
```

The multi-container `docker-compose.multi.yml` stack is still available when
you prefer one container per identity (isolated GitHub rate-limit budgets,
independent rotation); each service then sets its own `GITHUB_MCP_AUTH_TOKEN`.

**How clients tell the identities apart.** MCP itself has no identity field,
so distinction happens in the client config: one `mcpServers` entry per
identity, with its own name, port and Bearer key. Tools are namespaced by
entry name (`mcp__github-personal__get_me` vs `mcp__github-bot__get_me`), so
the agent picks the identity by name. After connecting, call `get_me` on each
server once — it reports the authenticated account backing that instance.

```json
{
  "mcpServers": {
    "github-personal": {
      "url": "http://your-server:8080/mcp",
      "headers": { "Authorization": "Bearer <key-personal>" }
    },
    "github-bot": {
      "url": "http://your-server:8080/mcp",
      "headers": { "Authorization": "Bearer <key-bot>" }
    }
  }
}
```

## Run locally (stdio, e.g. for IDE integrations)

```bash
export GITHUB_PERSONAL_ACCESS_TOKEN=github_pat_xxx
./bin/go-github-mcp            # stdio transport
MCP_TRANSPORT=http GITHUB_MCP_AUTH_TOKEN=local:github_pat_xxx ./bin/go-github-mcp   # http transport
```

### Claude Desktop / VS Code / Cursor (stdio)

```json
{
  "mcpServers": {
    "github": {
      "command": "docker",
      "args": ["run", "-i", "--rm",
               "-e", "GITHUB_PERSONAL_ACCESS_TOKEN",
               "ghcr.io/agogo233/go-github-mcp:latest"],
      "env": { "GITHUB_PERSONAL_ACCESS_TOKEN": "<your-token>" }
    }
  }
}
```

### Remote MCP (HTTP)

```json
{
  "mcpServers": {
    "github": {
      "url": "http://your-server:8080/mcp",
      "headers": { "Authorization": "Bearer <key>", "X-Client-Name": "my-agent" }
    }
  }
}
```

## Tools (M4)

Enabled by default (`GITHUB_TOOLSETS=default`): `context`, `users`, `repos`,
`issues`, `pull_requests`, `actions`. All other toolsets must be enabled
explicitly, e.g. `GITHUB_TOOLSETS=default,git,labels` or `GITHUB_TOOLSETS=all`.
The default set includes the actions write tools `rerun_workflow` and
`cancel_workflow_run`; disable them with `READ_ONLY=true` or by trimming
`GITHUB_TOOLSETS`.

| Toolset | Tools |
|---|---|
| context | `get_me` |
| users | `get_user`, `search_users` |
| repos | `get_repository`, `search_repositories`, `search_code`, `search_commits`, `get_file_contents`, `create_or_update_file`, `delete_file`, `list_releases`, `get_latest_release`, `get_release_by_tag`, `list_tags`, `get_tag`, `list_repository_collaborators`, `create_repository`, `delete_repository`, `fork_repository` |
| git | `list_branches`, `create_branch`, `list_commits`, `get_commit`, `get_blob`, `get_tree`, `push_files` |
| issues | `list_issues`, `get_issue`, `create_issue`, `update_issue`, `add_issue_comment`, `list_issue_comments`, `update_issue_comment`, `delete_issue_comment`, `search_issues`, `add_sub_issue`, `remove_sub_issue`, `list_issue_types` |
| labels | `list_labels`, `get_label`, `create_label`, `update_label`, `delete_label`, `add_issue_labels`, `remove_issue_label` |
| pull_requests | `list_pull_requests`, `get_pull_request`, `create_pull_request`, `update_pull_request`, `merge_pull_request`, `get_pull_request_files`, `get_pull_request_reviews`, `add_pull_request_comment`, `get_pull_request_status`, `search_pull_requests` |
| actions | `list_workflows`, `get_workflow`, `list_workflow_runs`, `get_workflow_run`, `list_workflow_jobs`, `get_workflow_run_logs`, `get_job_logs`, `list_workflow_run_artifacts`, `get_workflow_run_artifact`, `rerun_workflow`, `cancel_workflow_run` |
| notifications | `list_notifications`, `get_notification_thread`, `mark_thread_read` |
| gists | `list_gists`, `get_gist`, `create_gist`, `update_gist`, `delete_gist` |
| discussions | `list_discussions`, `get_discussion`, `create_discussion`, `list_discussion_comments`, `add_discussion_comment`, `list_discussion_categories` |
| stargazers | `list_stargazers`, `list_starred_repos` |
| orgs | `get_organization`, `list_org_members`, `list_org_teams`, `list_org_repositories` |
| code_security | `list_code_scanning_alerts`, `get_code_scanning_alert`, `update_code_scanning_alert` |
| secret_protection | `list_secret_scanning_alerts`, `get_secret_scanning_alert`, `list_secret_scanning_locations`, `update_secret_scanning_alert` |
| dependabot | `list_dependabot_alerts`, `get_dependabot_alert`, `update_dependabot_alert` |
| security_advisories | `list_repository_security_advisories`, `get_security_advisory` |
| projects | `list_projects`, `list_project_items` (Projects v2 via GraphQL; github.com) |

Repository lifecycle tools (`create_repository`, `delete_repository`,
`fork_repository`) need elevated token permissions (e.g. `delete_repo` for
deletion). Trim them with `GITHUB_TOOLSETS`/`GITHUB_TOOLS` if your token does
not have those rights.

`push_files` performs API-based multi-file commits (blob → tree → commit →
ref update), so no git credentials are ever needed in the container.

### Not provided (by design)

These common write operations deliberately have no tool yet; add them only
when a real workflow needs them, since every write tool widens what a leaked
token can do: creating, editing or deleting releases; deleting branches;
submitting or dismissing pull request reviews; updating repository metadata
or settings; organization and team management; deleting issues or
discussions.

## Development

```bash
make test     # go test -race + coverage
make lint     # golangci-lint
make build    # binary with version injection
make docker   # local image build
```

CI runs lint + tests + build on pushes and PRs that touch Go code or CI
configuration, and publishes the image to
`ghcr.io/agogo233/go-github-mcp` on `main` pushes and `v*` tags.

A manual **Smoke** workflow (`workflow_dispatch`) runs the built binary
end-to-end against the real GitHub API with read-only calls only (stdio
handshake + `get_me`, HTTP auth checks, insecure-startup refusal). It requires
the `SMOKE_GITHUB_TOKEN` repository secret; without it the job fails with an
explicit message.

## License

[MIT](./LICENSE)
