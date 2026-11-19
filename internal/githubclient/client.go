// Package githubclient builds and verifies the GitHub API client.
package githubclient

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/go-github/v72/github"

	"github.com/agogo233/go-github-mcp/internal/config"
)

const httpTimeout = 30 * time.Second

// New builds a *github.Client from configuration.
func New(cfg *config.Config) (*github.Client, error) {
	httpClient := &http.Client{Timeout: httpTimeout, Transport: retryTransport{base: http.DefaultTransport}}

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
// when the PAT is revoked, expired, or lacks the required scope. It returns
// the authenticated login so the caller owns all logging.
func Verify(ctx context.Context, client *github.Client) (string, error) {
	user, _, err := client.Users.Get(ctx, "")
	if err != nil {
		return "", fmt.Errorf("token self-check against /user failed: %w%s", err, verifyHint(err))
	}
	login := ""
	if user != nil && user.Login != nil {
		login = *user.Login
	}
	return login, nil
}

// verifyHint turns common /user failure statuses into an actionable hint.
func verifyHint(err error) string {
	var eresp *github.ErrorResponse
	if errors.As(err, &eresp) && eresp.Response != nil {
		switch eresp.Response.StatusCode {
		case http.StatusUnauthorized:
			return " (the token was rejected; check that it is valid and not expired)"
		case http.StatusForbidden:
			return " (the token lacks permission; fine-grained tokens need the Profile read permission for /user)"
		case http.StatusNotFound:
			return " (/user was not found; check GITHUB_HOST — leave it unset for github.com)"
		}
	}
	return ""
}

// retryTransport retries idempotent GET requests a limited number of times on
// transient failures (connection errors, 5xx) with exponential backoff.
// Non-GET requests are never retried to avoid duplicate side effects.
type retryTransport struct {
	base http.RoundTripper
}

func (t retryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method != http.MethodGet {
		return t.base.RoundTrip(req)
	}
	delays := []time.Duration{200 * time.Millisecond, 400 * time.Millisecond}
	var resp *http.Response
	var err error
	for attempt := 0; ; attempt++ {
		resp, err = t.base.RoundTrip(req)
		if attempt >= len(delays) {
			break
		}
		if err == nil && resp.StatusCode < http.StatusInternalServerError {
			break
		}
		if err == nil {
			// Drain and close so the connection can be reused on retry.
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}
		select {
		case <-time.After(delays[attempt]):
		case <-req.Context().Done():
			return nil, req.Context().Err()
		}
	}
	return resp, err
}
