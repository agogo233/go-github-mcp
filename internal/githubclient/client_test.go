package githubclient

import (
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
