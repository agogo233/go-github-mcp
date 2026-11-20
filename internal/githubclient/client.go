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

// tokenCtxKey carries the per-request GitHub PAT through the request context.
// It is set by the HTTP auth middleware and read by tokenInjectTransport when
// the MCP tool handler's context reaches an outbound GitHub request.
type tokenCtxKey struct{}

// WithContextToken returns a context carrying the GitHub PAT to use for the
// API calls that derive from it. Pass the resulting context to go-github
// methods (or Verify) to act as that identity.
func WithContextToken(ctx context.Context, token string) context.Context {
	return context.WithValue(ctx, tokenCtxKey{}, token)
}

// TokenFromContext returns the per-request GitHub PAT set by WithContextToken,
// or "" when none is present.
func TokenFromContext(ctx context.Context) string {
	t, _ := ctx.Value(tokenCtxKey{}).(string)
	return t
}

// New builds a *github.Client from configuration. The client carries no
// static PAT: authentication is injected per outbound request by
// tokenInjectTransport, which reads the PAT from the request context and
// falls back to the configured default token. This lets a single client serve
// every identity behind the HTTP transport.
func New(cfg *config.Config) (*github.Client, error) {
	httpClient := &http.Client{
		Timeout: httpTimeout,
		Transport: tokenInjectTransport{
			base:     retryTransport{base: http.DefaultTransport},
			fallback: cfg.GitHubToken,
		},
	}

	if cfg.GitHubHost == "" {
		return github.NewClient(httpClient), nil
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
	// github.com and its API host must use the default client: go-github's
	// WithEnterpriseURLs would append "/api/v3/" to them, which is not a valid
	// REST endpoint.
	switch strings.ToLower(base.Hostname()) {
	case "github.com", "www.github.com", "api.github.com":
		return github.NewClient(httpClient), nil
	}
	upload, err := url.Parse(host + "/api/uploads/")
	if err != nil {
		return nil, fmt.Errorf("invalid GITHUB_HOST upload URL: %w", err)
	}
	rewriteGheComHost(base)
	rewriteGheComHost(upload)
	return github.NewClient(httpClient).WithEnterpriseURLs(base.String(), upload.String())
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

// tokenInjectTransport sets the Authorization header on every outbound
// request from the GitHub PAT carried in the request context, falling back to
// the configured default token when the context carries none. Cloning the
// request before mutation keeps the shared request object safe for reuse.
type tokenInjectTransport struct {
	base     http.RoundTripper
	fallback string // default PAT used when the context carries no token
}

func (t tokenInjectTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	token := TokenFromContext(req.Context())
	if token == "" {
		token = t.fallback
	}
	if token == "" {
		return t.base.RoundTrip(req)
	}
	req = req.Clone(req.Context())
	req.Header.Set("Authorization", "Bearer "+token)
	return t.base.RoundTrip(req)
}

// retryTransport retries idempotent GET requests a limited number of times on
// transient failures (connection errors, 5xx) with exponential backoff.
// Non-GET requests are never retried to avoid duplicate side effects.
// go-github's GET requests carry no body, so re-issuing the same request on
// retry is safe; if a GET with a body is ever introduced, rebuild it via
// req.GetBody before each retry.
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
			_ = resp.Body.Close()
		}
		select {
		case <-time.After(delays[attempt]):
		case <-req.Context().Done():
			return nil, req.Context().Err()
		}
	}
	return resp, err
}
