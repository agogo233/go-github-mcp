// Package githubclient builds and verifies the GitHub API client.
package githubclient

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/google/go-github/v72/github"

	"github.com/agogo233/go-github-mcp/internal/config"
)

const httpTimeout = 30 * time.Second

// New builds a *github.Client from configuration.
func New(cfg *config.Config) (*github.Client, error) {
	httpClient := &http.Client{Timeout: httpTimeout}

	if cfg.GitHubHost == "" {
		return github.NewClient(httpClient).WithAuthToken(cfg.GitHubToken), nil
	}

	host := cfg.GitHubHost
	if !strings.Contains(host, "://") {
		host = "https://" + host
	}
	host = strings.TrimSuffix(host, "/")
	base, err := url.Parse(host + "/")
	if err != nil {
		return nil, fmt.Errorf("invalid GITHUB_HOST %q: %w", cfg.GitHubHost, err)
	}
	upload, err := url.Parse(host + "/api/uploads/")
	if err != nil {
		return nil, fmt.Errorf("invalid GITHUB_HOST upload URL: %w", err)
	}
	rewriteGheComHost(base)
	rewriteGheComHost(upload)
	client, err := github.NewClient(httpClient).WithEnterpriseURLs(base.String(), upload.String())
	if err != nil {
		return nil, fmt.Errorf("invalid GITHUB_HOST %q: %w", cfg.GitHubHost, err)
	}
	return client.WithAuthToken(cfg.GitHubToken), nil
}

// rewriteGheComHost maps GitHub data-residency hosts (<sub>.ghe.com) to their
// API host (api.<sub>.ghe.com). go-github's WithEnterpriseURLs appends the
// "/api/v3/" path suffix to classic GHES hosts, but ghe.com serves the REST
// API directly from the api.<sub>.ghe.com root; rewriting the host before
// validation keeps that suffix off and points requests at the right endpoint.
func rewriteGheComHost(u *url.URL) {
	h := u.Hostname()
	if strings.HasSuffix(h, ".ghe.com") && !strings.HasPrefix(h, "api.") {
		u.Host = "api." + u.Host
	}
}

// Verify performs a lightweight authenticated call so the server fails fast
// when the PAT is revoked, expired, or lacks the required scope.
func Verify(ctx context.Context, client *github.Client) error {
	user, _, err := client.Users.Get(ctx, "")
	if err != nil {
		return fmt.Errorf("token self-check against /user failed: %w", err)
	}
	login := ""
	if user != nil && user.Login != nil {
		login = *user.Login
	}
	fmt.Fprintf(os.Stderr, "authenticated as %s\n", login)
	return nil
}
