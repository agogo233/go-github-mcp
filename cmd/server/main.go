// Command server runs the go-github-mcp MCP server over stdio or Streamable HTTP.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/agogo233/go-github-mcp/internal/config"
	"github.com/agogo233/go-github-mcp/internal/githubclient"
	"github.com/agogo233/go-github-mcp/internal/server"
	"github.com/agogo233/go-github-mcp/internal/transport"
)

// version is injected at build time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Fprintln(os.Stderr, version)
		return
	}

	// Bootstrap logger at info level so configuration errors are visible
	// even when LOG_LEVEL itself is invalid.
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))

	level, err := config.ParseLogLevel(os.Getenv("LOG_LEVEL"))
	if err != nil {
		slog.Error("invalid configuration", "error", err)
		os.Exit(1)
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: level,
	})))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load()
	if err != nil {
		slog.Error("invalid configuration", "error", err)
		os.Exit(1)
	}

	client, err := githubclient.New(cfg)
	if err != nil {
		slog.Error("failed to build GitHub client", "error", err)
		os.Exit(1)
	}
	login, err := githubclient.Verify(ctx, client)
	if err != nil {
		slog.Error("GitHub token self-check failed", "error", err)
		os.Exit(1)
	}
	slog.Info("GitHub token self-check passed", "login", login, "host", cfg.GitHubHostOrDefault())

	// Verify every configured identity up front so a revoked or expired PAT
	// fails fast at startup instead of on the first real call, and record each
	// login for audit attribution. Neither the PAT nor the MCP Bearer secret
	// is ever logged.
	logins := make(map[string]string, len(cfg.AuthTokens))
	for i, e := range cfg.AuthTokens {
		identityCtx := githubclient.WithContextToken(ctx, e.PAT)
		l, err := githubclient.Verify(identityCtx, client)
		if err != nil {
			// entry_index is safe to log; the key itself never is.
			slog.Error("GitHub identity token self-check failed", "entry_index", i+1, "error", err)
			os.Exit(1)
		}
		logins[e.Key] = l
		slog.Info("GitHub identity self-check passed", "login", l, "host", cfg.GitHubHostOrDefault())
	}

	mcpServer, err := server.New(cfg, client, version)
	if err != nil {
		slog.Error("failed to initialize MCP server", "error", err)
		os.Exit(1)
	}

	switch cfg.Transport {
	case config.TransportHTTP:
		slog.Info("starting HTTP transport", "addr", cfg.HTTPAddr, "read_only", cfg.ReadOnly)
		if err := transport.RunHTTP(ctx, mcpServer, cfg, logins); err != nil {
			slog.Error("HTTP server exited with error", "error", err)
			os.Exit(1)
		}
	default:
		slog.Info("starting stdio transport", "read_only", cfg.ReadOnly)
		errCh := make(chan error, 1)
		go func() { errCh <- transport.RunStdio(mcpServer) }()
		select {
		case err := <-errCh:
			if err != nil {
				slog.Error("stdio server exited with error", "error", err)
				os.Exit(1)
			}
		case <-ctx.Done():
			// SIGINT/SIGTERM: exit gracefully; stdio ends with the process.
		}
	}
}
