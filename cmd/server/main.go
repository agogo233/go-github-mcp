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

	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
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
	if err := githubclient.Verify(ctx, client); err != nil {
		slog.Error("GitHub token self-check failed", "error", err)
		os.Exit(1)
	}
	slog.Info("GitHub token self-check passed", "host", cfg.GitHubHostOrDefault())

	mcpServer := server.New(cfg, client, version)

	switch cfg.Transport {
	case config.TransportHTTP:
		slog.Info("starting HTTP transport", "addr", cfg.HTTPAddr, "read_only", cfg.ReadOnly)
		if err := transport.RunHTTP(ctx, mcpServer, cfg); err != nil {
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
