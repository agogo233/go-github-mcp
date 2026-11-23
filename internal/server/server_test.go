package server

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/go-github/v72/github"
	mcp "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"github.com/mark3labs/mcp-go/server/servertest"

	"github.com/agogo233/go-github-mcp/internal/config"
)

func TestToolAllowed(t *testing.T) {
	base := &config.Config{Toolsets: []string{"default"}}
	tests := []struct {
		name     string
		cfg      *config.Config
		toolset  string
		tool     string
		writable bool
		want     bool
	}{
		{"default toolset read tool", base, "issues", "list_issues", false, true},
		{"default toolset write tool", base, "issues", "create_issue", true, true},
		{"non-default toolset disabled", base, "git", "list_branches", false, false},
		{"read-only drops write tool", &config.Config{ReadOnly: true, Toolsets: []string{"all"}}, "issues", "create_issue", true, false},
		{"read-only keeps read tool", &config.Config{ReadOnly: true, Toolsets: []string{"all"}}, "issues", "list_issues", false, true},
		{"all enables every toolset", &config.Config{Toolsets: []string{"all"}}, "actions", "list_workflows", true, true},
		{"explicit extra toolset", &config.Config{Toolsets: []string{"default", "labels"}}, "labels", "list_labels", false, true},
		{"github_tools blocks unlisted tool", &config.Config{Toolsets: []string{"all"}, Tools: []string{"get_me"}}, "issues", "list_issues", false, false},
		{"github_tools allow-list hit", &config.Config{Toolsets: []string{"all"}, Tools: []string{"create_issue"}}, "issues", "create_issue", true, true},
		{"github_tools cannot bypass read-only", &config.Config{ReadOnly: true, Toolsets: []string{"all"}, Tools: []string{"create_issue"}}, "issues", "create_issue", true, false},
		{"github_tools cannot bypass toolsets", &config.Config{Toolsets: []string{"default"}, Tools: []string{"list_branches"}}, "git", "list_branches", false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, reason := toolAllowed(tt.cfg, tt.toolset, tt.tool, tt.writable)
			if got != tt.want {
				t.Fatalf("toolAllowed(%q, %q, writable=%v) = %v (reason %q), want %v",
					tt.toolset, tt.tool, tt.writable, got, reason, tt.want)
			}
			if !got && reason == "" {
				t.Error("rejected tool must carry a reason")
			}
		})
	}
}

func TestValidToolsetsHelpMatchesKnownToolsets(t *testing.T) {
	listed := map[string]bool{}
	for _, name := range strings.Split(validToolsetsHelp, ", ") {
		listed[name] = true
	}
	for name := range knownToolsets {
		if !listed[name] {
			t.Errorf("toolset %q is in knownToolsets but missing from validToolsetsHelp", name)
		}
	}
	for name := range listed {
		if !knownToolsets[name] {
			t.Errorf("validToolsetsHelp lists %q which is not in knownToolsets", name)
		}
	}
}

func TestNewRejectsUnknownToolset(t *testing.T) {
	cfg := &config.Config{Toolsets: []string{"default", "pullrequests"}}
	s, err := New(cfg, github.NewClient(nil), "test")
	if err == nil {
		t.Fatal("expected error for unknown toolset, got nil")
	}
	if s != nil {
		t.Error("server must be nil when configuration is rejected")
	}
	if !strings.Contains(err.Error(), "pullrequests") {
		t.Errorf("error should name the offending toolset, got: %v", err)
	}
}

func TestNewAcceptsValidToolsets(t *testing.T) {
	cfg := &config.Config{Toolsets: []string{"default", "git", "labels", "actions"}}
	s, err := New(cfg, github.NewClient(nil), "test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s == nil {
		t.Fatal("expected non-nil server")
	}
}

// countingRegistrar implements tools.Registrar and counts registered tools.
type countingRegistrar struct{ n int }

func (c *countingRegistrar) Add(string, bool, mcp.Tool, mcpserver.ToolHandlerFunc) { c.n++ }

func TestToolsetRegistryIsComplete(t *testing.T) {
	if len(toolsetRegistry) != 18 {
		t.Fatalf("toolsetRegistry has %d entries, want 18", len(toolsetRegistry))
	}
	seen := map[string]bool{}
	for _, e := range toolsetRegistry {
		if e.name == "" || e.register == nil {
			t.Errorf("registry entry %q must have a name and a register func", e.name)
		}
		if seen[e.name] {
			t.Errorf("duplicate toolset %q in registry", e.name)
		}
		seen[e.name] = true
	}
}

// The registry is the single source of truth; this pins the documented
// contract of 18 toolsets registering 104 tools in total.
func TestRegistryRegistersAllTools(t *testing.T) {
	c := &countingRegistrar{}
	for _, e := range toolsetRegistry {
		e.register(c, github.NewClient(nil))
	}
	if c.n != 104 {
		t.Fatalf("registry registered %d tools, want 104", c.n)
	}
}

// captureLogs redirects the default slog logger into a buffer at the given
// level for the duration of the test and restores it on cleanup.
func captureLogs(t *testing.T, level slog.Level) *bytes.Buffer {
	t.Helper()
	buf := &bytes.Buffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: level})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf
}

var noopHandler mcpserver.ToolHandlerFunc = func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return nil, nil
}

// logFieldValue extracts the value of "key=value" from a slog text line.
func logFieldValue(line, key string) string {
	for _, field := range strings.Fields(line) {
		if k, v, ok := strings.Cut(field, "="); ok && k == key {
			return v
		}
	}
	return ""
}

func TestRegistrarSummaryCounts(t *testing.T) {
	cfg := &config.Config{ReadOnly: true, Toolsets: []string{"issues"}, Tools: []string{"listed", "elsewhere"}}
	r := &Registrar{s: mcpserver.NewMCPServer("test", "test"), cfg: cfg}
	buf := captureLogs(t, slog.LevelInfo)

	r.Add("issues", false, mcp.NewTool("listed"), noopHandler)   // registered
	r.Add("issues", true, mcp.NewTool("elsewhere"), noopHandler) // read-only skip
	r.Add("actions", false, mcp.NewTool("listed"), noopHandler)  // toolset-disabled skip
	r.Add("issues", false, mcp.NewTool("ghost"), noopHandler)    // allow-list skip
	r.logSummary()

	out := buf.String()
	for _, want := range []string{
		"registered=1",
		"skipped_read_only=1",
		"skipped_toolset_disabled=1",
		"skipped_allow_list=1",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("summary missing %s, got:\n%s", want, out)
		}
	}
}

func TestNewLogsSingleSummaryInsteadOfPerToolSkips(t *testing.T) {
	cfg := &config.Config{ReadOnly: true, Toolsets: []string{"default"}}
	buf := captureLogs(t, slog.LevelInfo)
	if _, err := New(cfg, github.NewClient(nil), "test"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := buf.String()
	if n := strings.Count(out, "tool registration complete"); n != 1 {
		t.Fatalf("expected exactly one summary line, got %d:\n%s", n, out)
	}
	if strings.Contains(out, "skipping tool excluded by policy") {
		t.Errorf("per-tool skip lines must not appear at info level:\n%s", out)
	}
	// The summary must account for every registry tool (104, pinned above).
	line := ""
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		if strings.Contains(l, "tool registration complete") {
			line = l
			break
		}
	}
	sum := 0
	for _, key := range []string{"registered", "skipped_read_only", "skipped_toolset_disabled", "skipped_allow_list"} {
		v, err := strconv.Atoi(logFieldValue(line, key))
		if err != nil {
			t.Fatalf("summary line %q missing integer field %s", line, key)
		}
		sum += v
	}
	if sum != 104 {
		t.Errorf("summary accounts for %d tools, want 104", sum)
	}
}

func TestSkippedToolsLoggedAtDebugLevel(t *testing.T) {
	cfg := &config.Config{ReadOnly: true, Toolsets: []string{"default"}}
	buf := captureLogs(t, slog.LevelDebug)
	if _, err := New(cfg, github.NewClient(nil), "test"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "skipping tool excluded by policy") {
		t.Errorf("debug level should keep per-tool skip details, got:\n%s", out)
	}
	if !strings.Contains(out, reasonReadOnly) {
		t.Errorf("debug lines should carry the skip reason, got:\n%s", out)
	}
}

func TestExplicitlyListedExcludedToolWarns(t *testing.T) {
	cfg := &config.Config{ReadOnly: true, Toolsets: []string{"all"}, Tools: []string{"create_issue"}}
	buf := captureLogs(t, slog.LevelInfo)
	r := &Registrar{s: mcpserver.NewMCPServer("test", "test"), cfg: cfg}
	r.Add("issues", true, mcp.NewTool("create_issue"), noopHandler)

	out := buf.String()
	if !strings.Contains(out, "tool listed in GITHUB_TOOLS is excluded by policy") {
		t.Errorf("expected warn for explicitly listed excluded tool, got:\n%s", out)
	}
	if !strings.Contains(out, "tool=create_issue") {
		t.Errorf("warn should name the offending tool, got:\n%s", out)
	}
}

func TestBuildInstructionsReflectsReadOnly(t *testing.T) {
	ro := buildInstructions(&config.Config{ReadOnly: true, Toolsets: []string{"default"}})
	for _, want := range []string{"READ-ONLY", "get_me", "owner", "per_page"} {
		if !strings.Contains(ro, want) {
			t.Errorf("read-only instructions missing %q, got:\n%s", want, ro)
		}
	}

	rw := buildInstructions(&config.Config{Toolsets: []string{"default"}})
	if strings.Contains(rw, "READ-ONLY") {
		t.Errorf("writable instructions must not claim read-only mode, got:\n%s", rw)
	}
	if !strings.Contains(rw, "Write tools are available") {
		t.Errorf("writable instructions should state write availability, got:\n%s", rw)
	}
}

// The instructions must actually reach agents: an initialize handshake over
// the real HTTP transport has to return them in result.instructions.
func TestInitializeReturnsInstructions(t *testing.T) {
	cfg := &config.Config{ReadOnly: true, Toolsets: []string{"default"}}
	s, err := New(cfg, github.NewClient(nil), "test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	ts := servertest.NewTestStreamableHTTPServer(s)
	defer ts.Close()
	captureLogs(t, slog.LevelInfo) // keep the startup summary out of test output

	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{` +
		`"protocolVersion":"2025-11-25","capabilities":{},` +
		`"clientInfo":{"name":"test-client","version":"1.0"}}}`
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/mcp", strings.NewReader(body))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("initialize request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("initialize status = %d, want 200", resp.StatusCode)
	}

	var parsed struct {
		Result struct {
			Instructions string `json:"instructions"`
		} `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		t.Fatalf("decode initialize response: %v", err)
	}
	if parsed.Result.Instructions == "" {
		t.Fatal("initialize response must carry non-empty instructions")
	}
	for _, want := range []string{"get_me", "READ-ONLY"} {
		if !strings.Contains(parsed.Result.Instructions, want) {
			t.Errorf("instructions missing %q, got:\n%s", want, parsed.Result.Instructions)
		}
	}
}
