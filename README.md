# go-github-mcp

[![CI](https://github.com/agogo233/go-github-mcp/actions/workflows/ci.yml/badge.svg)](https://github.com/agogo233/go-github-mcp/actions/workflows/ci.yml)
[![Docker](https://github.com/agogo233/go-github-mcp/actions/workflows/docker.yml/badge.svg)](https://github.com/agogo233/go-github-mcp/actions/workflows/docker.yml)

A single-tenant **GitHub MCP Server** written in Go, shipped as a Docker image.
It lets MCP clients (Claude, VS Code, Cursor, OpenCode, custom agents, ...)
operate on GitHub through the REST/GraphQL APIs while the **personal access
token (PAT) never leaves the server container**.

> Status: M1 skeleton — `stdio` + Streamable `HTTP` transports, config
> hardening, toolset/read-only policy framework, and the `get_me` tool.
> More toolsets (repos, issues, pull_requests, actions, security, ...) land in
> upcoming milestones.

## Why

- **Team-shared service**: deploy once on an internal server; every teammate's
  MCP client connects over Streamable HTTP with a shared secret. The GitHub
  PAT lives only inside the container.
- **Auditable**: every HTTP request is logged with the self-reported
  `X-Client-Name` header (operational insight, not strong attribution).
- **Least privilege**: toolsets can be trimmed via `GITHUB_TOOLSETS`, write
  tools can be globally disabled with `READ_ONLY=true`.

## Security model

| Layer | Control |
|---|---|
| Token storage | PAT injected via environment/secret only; never written to disk, never logged |
| Client side | MCP clients only know the server URL + shared secret; they never see the PAT |
| Transport auth | `Authorization: Bearer <AUTH_TOKEN>` enforced on every endpoint (constant-time compare) |
| Startup guard | Token self-check against `/user`; HTTP mode refuses to start without `AUTH_TOKEN` |
| Blast radius | `GITHUB_TOOLSETS` / `GITHUB_TOOLS` allow-list, `READ_ONLY` kill switch for all write tools |
| GHES | `GITHUB_HOST` must be https (loopback http tolerated for local dev) |

If the PAT is ever compromised: revoke it on GitHub, rotate
`GITHUB_PERSONAL_ACCESS_TOKEN`, restart the container, and review audit logs.

## Configuration

| Variable | Required | Default | Description |
|---|---|---|---|
| `GITHUB_PERSONAL_ACCESS_TOKEN` | yes | – | GitHub PAT (fine-grained recommended) |
| `MCP_TRANSPORT` | no | `stdio` | `stdio` or `http` |
| `HTTP_ADDR` | no | `0.0.0.0:8080` | HTTP listen address (HTTP mode) |
| `AUTH_TOKEN` | http mode | – | Shared secret for Bearer auth (required in HTTP mode) |
| `ALLOW_UNAUTHENTICATED` | no | `false` | Local-debugging escape hatch to skip Bearer auth |
| `GITHUB_HOST` | no | `github.com` | GHES / ghe.com hostname (https enforced) |
| `GITHUB_TOOLSETS` | no | `default` | Comma list: `default`, `all`, or named toolsets |
| `GITHUB_TOOLS` | no | – | Comma list of individual tool names to add |
| `READ_ONLY` | no | `false` | Skip registration of all write tools |

## Run with Docker (team-shared HTTP mode)

```bash
cat > .env <<'EOF'
GITHUB_PERSONAL_ACCESS_TOKEN=github_pat_xxx
AUTH_TOKEN=change-me-shared-secret
EOF

docker compose up -d
curl http://127.0.0.1:8080/healthz   # -> ok
```

MCP endpoint: `http://<host>:8080/mcp` (Streamable HTTP).
For LAN/team use, terminate TLS with a reverse proxy (nginx/Caddy) in front.

## Run locally (stdio, e.g. for IDE integrations)

```bash
export GITHUB_PERSONAL_ACCESS_TOKEN=github_pat_xxx
./bin/go-github-mcp            # stdio transport
MCP_TRANSPORT=http AUTH_TOKEN=s3cret ./bin/go-github-mcp   # http transport
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
      "headers": { "Authorization": "Bearer <AUTH_TOKEN>", "X-Client-Name": "my-agent" }
    }
  }
}
```

## Tools (M1)

| Toolset | Tool | Description |
|---|---|---|
| context | `get_me` | Details of the authenticated GitHub account |

Planned: `repos`, `git` (API-based push via `push_files`), `issues`, `labels`,
`pull_requests`, `actions`, `notifications`, `gists`, `discussions`,
`stargazers`, `orgs`, `code_security`, `secret_protection`, `dependabot`,
`security_advisories`, `projects`.

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

## License

[MIT](./LICENSE)
