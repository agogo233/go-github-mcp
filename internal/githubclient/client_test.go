package githubclient

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/agogo233/go-github-mcp/internal/config"
)

func TestNewDefaultHost(t *testing.T) {
	c, err := New(&config.Config{GitHubToken: "t"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := c.BaseURL.String(); got != "https://api.github.com/" {
		t.Errorf("BaseURL = %q, want https://api.github.com/", got)
	}
}

func TestNewClassicGHES(t *testing.T) {
	c, err := New(&config.Config{GitHubToken: "t", GitHubHost: "ghe.example.com"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := c.BaseURL.String(); got != "https://ghe.example.com/api/v3/" {
		t.Errorf("BaseURL = %q, want https://ghe.example.com/api/v3/", got)
	}
	if got := c.UploadURL.String(); got != "https://ghe.example.com/api/uploads/" {
		t.Errorf("UploadURL = %q, want https://ghe.example.com/api/uploads/", got)
	}
}

// ghe.com data-residency hosts must point at api.<sub>.ghe.com without the
// "/api/v3/" suffix that classic GHES hosts get.
func TestNewGheComDataResidency(t *testing.T) {
	c, err := New(&config.Config{GitHubToken: "t", GitHubHost: "sub.ghe.com"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := c.BaseURL.String(); got != "https://api.sub.ghe.com/" {
		t.Errorf("BaseURL = %q, want https://api.sub.ghe.com/", got)
	}
	if got := c.UploadURL.String(); got != "https://api.sub.ghe.com/api/uploads/" {
		t.Errorf("UploadURL = %q, want https://api.sub.ghe.com/api/uploads/", got)
	}
}

func TestNewGheComAlreadyApiHost(t *testing.T) {
	c, err := New(&config.Config{GitHubToken: "t", GitHubHost: "api.sub.ghe.com"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := c.BaseURL.String(); got != "https://api.sub.ghe.com/" {
		t.Errorf("BaseURL = %q, want https://api.sub.ghe.com/ (no double prefix)", got)
	}
}

// An explicit github.com host must use the default client; WithEnterpriseURLs
// would wrongly append "/api/v3/" to it.
func TestNewExplicitGithubCom(t *testing.T) {
	for _, host := range []string{"github.com", "https://github.com", "www.github.com", "api.github.com", "GitHub.com", "API.GitHub.COM"} {
		c, err := New(&config.Config{GitHubToken: "t", GitHubHost: host})
		if err != nil {
			t.Fatalf("host %q: unexpected error: %v", host, err)
		}
		if got := c.BaseURL.String(); got != "https://api.github.com/" {
			t.Errorf("host %q: BaseURL = %q, want https://api.github.com/", host, got)
		}
	}
}

// flakyRT fails the first N GET calls with a 500, then succeeds.
type flakyRT struct{ remaining int }

func (f *flakyRT) RoundTrip(_ *http.Request) (*http.Response, error) {
	if f.remaining > 0 {
		f.remaining--
		return &http.Response{StatusCode: http.StatusInternalServerError, Body: io.NopCloser(strings.NewReader(""))}, nil
	}
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("ok"))}, nil
}

func TestRetryTransportRetriesGetOn5xx(t *testing.T) {
	base := &flakyRT{remaining: 1}
	resp, err := retryTransport{base: base}.RoundTrip(httptest.NewRequest(http.MethodGet, "https://example.com", nil))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if base.remaining != 0 {
		t.Errorf("expected one retry, remaining = %d", base.remaining)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200 after retry", resp.StatusCode)
	}
}

func TestRetryTransportDoesNotRetryNonGet(t *testing.T) {
	base := &flakyRT{remaining: 5}
	resp, err := retryTransport{base: base}.RoundTrip(httptest.NewRequest(http.MethodPost, "https://example.com", nil))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if base.remaining != 4 {
		t.Errorf("POST must not be retried, remaining = %d", base.remaining)
	}
	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500 passed through", resp.StatusCode)
	}
}

type failingRT struct{ calls int }

func (f *failingRT) RoundTrip(_ *http.Request) (*http.Response, error) {
	f.calls++
	return nil, errors.New("connection reset")
}

func TestRetryTransportBailsOutAfterBudget(t *testing.T) {
	base := &failingRT{}
	_, err := retryTransport{base: base}.RoundTrip(httptest.NewRequest(http.MethodGet, "https://example.com", nil))
	if err == nil {
		t.Fatal("expected the connection error to surface")
	}
	if base.calls != 3 { // initial + 2 retries
		t.Errorf("calls = %d, want 3", base.calls)
	}
}

// captureAuthRT records the Authorization header of every request it sees.
type captureAuthRT struct {
	header string
}

func (c *captureAuthRT) RoundTrip(req *http.Request) (*http.Response, error) {
	c.header = req.Header.Get("Authorization")
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("ok"))}, nil
}

func TestTokenInjectTransportUsesContextToken(t *testing.T) {
	base := &captureAuthRT{}
	tr := tokenInjectTransport{base: base, fallback: "default-pat"}
	req := httptest.NewRequest(http.MethodGet, "https://example.com", nil)
	req = req.WithContext(WithContextToken(req.Context(), "ctx-pat"))
	if _, err := tr.RoundTrip(req); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if base.header != "Bearer ctx-pat" {
		t.Errorf("Authorization = %q, want %q", base.header, "Bearer ctx-pat")
	}
}

func TestTokenInjectTransportFallsBackToDefault(t *testing.T) {
	base := &captureAuthRT{}
	tr := tokenInjectTransport{base: base, fallback: "default-pat"}
	req := httptest.NewRequest(http.MethodGet, "https://example.com", nil)
	if _, err := tr.RoundTrip(req); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if base.header != "Bearer default-pat" {
		t.Errorf("Authorization = %q, want %q", base.header, "Bearer default-pat")
	}
}

func TestTokenInjectTransportClonesRequest(t *testing.T) {
	base := &captureAuthRT{}
	tr := tokenInjectTransport{base: base, fallback: "default-pat"}
	req := httptest.NewRequest(http.MethodGet, "https://example.com", nil)
	if _, err := tr.RoundTrip(req); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// The original request must not have been mutated by the transport.
	if got := req.Header.Get("Authorization"); got != "" {
		t.Errorf("original request Authorization = %q, want empty (must be cloned)", got)
	}
}

func TestTokenFromContext(t *testing.T) {
	if got := TokenFromContext(context.Background()); got != "" {
		t.Errorf("TokenFromContext(background) = %q, want empty", got)
	}
	ctx := WithContextToken(context.Background(), "tok")
	if got := TokenFromContext(ctx); got != "tok" {
		t.Errorf("TokenFromContext = %q, want tok", got)
	}
}
