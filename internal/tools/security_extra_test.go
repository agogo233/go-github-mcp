package tools

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestListCodeScanningAlerts(t *testing.T) {
	client, calls := newFakeGitHub(t, func(r *http.Request) (int, string, http.Header) {
		if r.URL.Query().Get("state") != "open" {
			t.Errorf("state query = %q, want open", r.URL.Query().Get("state"))
		}
		return http.StatusOK, `[{"number":1,"state":"open","rule":{"id":"js-xss"},"tool":{"name":"CodeQL"}}]`, nil
	})
	r := newRecordingRegistrar()
	RegisterCodeSecurity(r, client)

	text, isErr := callTool(t, r, "list_code_scanning_alerts", map[string]any{"owner": "o", "repo": "r"})
	if isErr {
		t.Fatalf("unexpected error result: %s", text)
	}
	if !strings.Contains(text, "js-xss") || !strings.Contains(text, "CodeQL") {
		t.Errorf("unexpected payload: %s", text)
	}
	if len(*calls) != 1 || !strings.HasSuffix((*calls)[0], "GET /api/v3/repos/o/r/code-scanning/alerts") {
		t.Errorf("unexpected calls: %v", *calls)
	}
}

func TestGetCodeScanningAlert(t *testing.T) {
	client, calls := newFakeGitHub(t, func(r *http.Request) (int, string, http.Header) {
		return http.StatusOK, `{"number":42,"state":"open","rule":{"id":"js-xss"},"most_recent_instance":{"ref":"refs/heads/main"}}`, nil
	})
	r := newRecordingRegistrar()
	RegisterCodeSecurity(r, client)

	text, isErr := callTool(t, r, "get_code_scanning_alert", map[string]any{"owner": "o", "repo": "r", "alert_id": float64(42)})
	if isErr {
		t.Fatalf("unexpected error result: %s", text)
	}
	if !strings.Contains(text, `"number":42`) || !strings.Contains(text, "refs/heads/main") {
		t.Errorf("unexpected payload: %s", text)
	}
	if !strings.HasSuffix((*calls)[0], "GET /api/v3/repos/o/r/code-scanning/alerts/42") {
		t.Errorf("unexpected calls: %v", *calls)
	}
}

func TestUpdateCodeScanningAlert(t *testing.T) {
	var body string
	client, calls := newFakeGitHub(t, func(r *http.Request) (int, string, http.Header) {
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		return http.StatusOK, `{"number":42,"state":"dismissed"}`, nil
	})
	r := newRecordingRegistrar()
	RegisterCodeSecurity(r, client)

	text, isErr := callTool(t, r, "update_code_scanning_alert", map[string]any{
		"owner": "o", "repo": "r", "alert_id": float64(42), "state": "dismissed",
	})
	if isErr {
		t.Fatalf("unexpected error result: %s", text)
	}
	if !strings.Contains(body, `"state":"dismissed"`) {
		t.Errorf("request body = %s", body)
	}
	if !strings.Contains(text, `"state":"dismissed"`) {
		t.Errorf("unexpected payload: %s", text)
	}
	if !strings.HasSuffix((*calls)[0], "PATCH /api/v3/repos/o/r/code-scanning/alerts/42") {
		t.Errorf("unexpected calls: %v", *calls)
	}
}

func TestListSecretScanningAlerts(t *testing.T) {
	client, calls := newFakeGitHub(t, func(_ *http.Request) (int, string, http.Header) {
		return http.StatusOK, `[{"number":7,"state":"open","secret_type":"github_personal_access_token"}]`, nil
	})
	r := newRecordingRegistrar()
	RegisterSecretProtection(r, client)

	text, isErr := callTool(t, r, "list_secret_scanning_alerts", map[string]any{"owner": "o", "repo": "r"})
	if isErr {
		t.Fatalf("unexpected error result: %s", text)
	}
	if !strings.Contains(text, "github_personal_access_token") {
		t.Errorf("unexpected payload: %s", text)
	}
	if !strings.HasSuffix((*calls)[0], "GET /api/v3/repos/o/r/secret-scanning/alerts") {
		t.Errorf("unexpected calls: %v", *calls)
	}
}

func TestGetSecretScanningAlert(t *testing.T) {
	client, calls := newFakeGitHub(t, func(_ *http.Request) (int, string, http.Header) {
		return http.StatusOK, `{"number":7,"state":"open","secret_type":"github_personal_access_token","html_url":"https://github.com/o/r/secret-scanning/7"}`, nil
	})
	r := newRecordingRegistrar()
	RegisterSecretProtection(r, client)

	text, isErr := callTool(t, r, "get_secret_scanning_alert", map[string]any{"owner": "o", "repo": "r", "alert_number": float64(7)})
	if isErr {
		t.Fatalf("unexpected error result: %s", text)
	}
	if !strings.Contains(text, `"number":7`) || !strings.Contains(text, "secret-scanning/7") {
		t.Errorf("unexpected payload: %s", text)
	}
	if !strings.HasSuffix((*calls)[0], "GET /api/v3/repos/o/r/secret-scanning/alerts/7") {
		t.Errorf("unexpected calls: %v", *calls)
	}
}

func TestListSecretScanningLocations(t *testing.T) {
	client, calls := newFakeGitHub(t, func(_ *http.Request) (int, string, http.Header) {
		return http.StatusOK, `[{"type":"commit","details":{"path":"config/app.yml","commit_sha":"abc123"}}]`, nil
	})
	r := newRecordingRegistrar()
	RegisterSecretProtection(r, client)

	text, isErr := callTool(t, r, "list_secret_scanning_locations", map[string]any{"owner": "o", "repo": "r", "alert_number": float64(7)})
	if isErr {
		t.Fatalf("unexpected error result: %s", text)
	}
	if !strings.Contains(text, "config/app.yml") || !strings.Contains(text, "abc123") {
		t.Errorf("unexpected payload: %s", text)
	}
	if !strings.HasSuffix((*calls)[0], "GET /api/v3/repos/o/r/secret-scanning/alerts/7/locations") {
		t.Errorf("unexpected calls: %v", *calls)
	}
}

func TestUpdateSecretScanningAlertRequiresResolutionInPayload(t *testing.T) {
	var body string
	client, calls := newFakeGitHub(t, func(r *http.Request) (int, string, http.Header) {
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		return http.StatusOK, `{"number":7,"state":"resolved","resolution":"false_positive"}`, nil
	})
	r := newRecordingRegistrar()
	RegisterSecretProtection(r, client)

	text, isErr := callTool(t, r, "update_secret_scanning_alert", map[string]any{
		"owner": "o", "repo": "r", "alert_number": float64(7),
		"state": "resolved", "resolution": "false_positive",
	})
	if isErr {
		t.Fatalf("unexpected error result: %s", text)
	}
	if !strings.Contains(body, `"state":"resolved"`) || !strings.Contains(body, `"resolution":"false_positive"`) {
		t.Errorf("request body = %s", body)
	}
	if !strings.HasSuffix((*calls)[0], "PATCH /api/v3/repos/o/r/secret-scanning/alerts/7") {
		t.Errorf("unexpected calls: %v", *calls)
	}
}

func TestListDependabotAlerts(t *testing.T) {
	client, calls := newFakeGitHub(t, func(r *http.Request) (int, string, http.Header) {
		if got := r.URL.Query().Get("severity"); got != "high" {
			t.Errorf("severity query = %q, want high", got)
		}
		return http.StatusOK, `[{"number":3,"state":"open","dependency":{"package":{"name":"lodash"},"manifest_path":"package.json"}}]`, nil
	})
	r := newRecordingRegistrar()
	RegisterDependabot(r, client)

	text, isErr := callTool(t, r, "list_dependabot_alerts", map[string]any{
		"owner": "o", "repo": "r", "severity": "high",
	})
	if isErr {
		t.Fatalf("unexpected error result: %s", text)
	}
	if !strings.Contains(text, "lodash") || !strings.Contains(text, "package.json") {
		t.Errorf("unexpected payload: %s", text)
	}
	if !strings.HasSuffix((*calls)[0], "GET /api/v3/repos/o/r/dependabot/alerts") {
		t.Errorf("unexpected calls: %v", *calls)
	}
}

func TestGetDependabotAlert(t *testing.T) {
	client, calls := newFakeGitHub(t, func(_ *http.Request) (int, string, http.Header) {
		return http.StatusOK, `{"number":3,"state":"open","security_advisory":{"ghsa_id":"GHSA-1","summary":"RCE"}}`, nil
	})
	r := newRecordingRegistrar()
	RegisterDependabot(r, client)

	text, isErr := callTool(t, r, "get_dependabot_alert", map[string]any{"owner": "o", "repo": "r", "alert_number": float64(3)})
	if isErr {
		t.Fatalf("unexpected error result: %s", text)
	}
	if !strings.Contains(text, "GHSA-1") || !strings.Contains(text, "RCE") {
		t.Errorf("unexpected payload: %s", text)
	}
	if !strings.HasSuffix((*calls)[0], "GET /api/v3/repos/o/r/dependabot/alerts/3") {
		t.Errorf("unexpected calls: %v", *calls)
	}
}

func TestUpdateDependabotAlert(t *testing.T) {
	var body string
	client, calls := newFakeGitHub(t, func(r *http.Request) (int, string, http.Header) {
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		return http.StatusOK, `{"number":3,"state":"dismissed","dismissed_reason":"inaccurate"}`, nil
	})
	r := newRecordingRegistrar()
	RegisterDependabot(r, client)

	text, isErr := callTool(t, r, "update_dependabot_alert", map[string]any{
		"owner": "o", "repo": "r", "alert_number": float64(3),
		"state": "dismissed", "dismissed_reason": "inaccurate", "dismissed_comment": "not used here",
	})
	if isErr {
		t.Fatalf("unexpected error result: %s", text)
	}
	for _, want := range []string{`"state":"dismissed"`, `"dismissed_reason":"inaccurate"`, `"dismissed_comment":"not used here"`} {
		if !strings.Contains(body, want) {
			t.Errorf("request body %s missing %s", body, want)
		}
	}
	if !strings.HasSuffix((*calls)[0], "PATCH /api/v3/repos/o/r/dependabot/alerts/3") {
		t.Errorf("unexpected calls: %v", *calls)
	}
}

func TestListRepositorySecurityAdvisories(t *testing.T) {
	client, calls := newFakeGitHub(t, func(_ *http.Request) (int, string, http.Header) {
		return http.StatusOK, `[{"ghsa_id":"GHSA-abcd-1234","summary":"SQL injection","html_url":"https://github.com/advisories/GHSA-abcd-1234"}]`, nil
	})
	r := newRecordingRegistrar()
	RegisterSecurityAdvisories(r, client)

	text, isErr := callTool(t, r, "list_repository_security_advisories", map[string]any{"owner": "o", "repo": "r"})
	if isErr {
		t.Fatalf("unexpected error result: %s", text)
	}
	if !strings.Contains(text, "SQL injection") || !strings.Contains(text, "GHSA-abcd-1234") {
		t.Errorf("unexpected payload: %s", text)
	}
	if !strings.HasSuffix((*calls)[0], "GET /api/v3/repos/o/r/security-advisories") {
		t.Errorf("unexpected calls: %v", *calls)
	}
}

func TestGetSecurityAdvisory(t *testing.T) {
	client, calls := newFakeGitHub(t, func(_ *http.Request) (int, string, http.Header) {
		return http.StatusOK, `{"ghsa_id":"GHSA-abcd-1234","summary":"SQL injection","severity":"high"}`, nil
	})
	r := newRecordingRegistrar()
	RegisterSecurityAdvisories(r, client)

	text, isErr := callTool(t, r, "get_security_advisory", map[string]any{"ghsa_id": "GHSA-abcd-1234"})
	if isErr {
		t.Fatalf("unexpected error result: %s", text)
	}
	if !strings.Contains(text, `"severity":"high"`) {
		t.Errorf("unexpected payload: %s", text)
	}
	if !strings.HasSuffix((*calls)[0], "GET /api/v3/advisories/GHSA-abcd-1234") {
		t.Errorf("unexpected calls: %v", *calls)
	}
}
