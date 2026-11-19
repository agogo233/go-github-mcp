package tools

import (
	"context"

	"github.com/google/go-github/v72/github"
	mcp "github.com/mark3labs/mcp-go/mcp"
)

// RegisterCodeSecurity registers the "code_security" toolset (code scanning).
func RegisterCodeSecurity(r Registrar, client *github.Client) {
	listAlerts := mcp.NewTool("list_code_scanning_alerts",
		mcp.WithDescription("List code scanning alerts of a repository."),
		withRepoArgs(),
		mcp.WithString("state", mcp.Description("Filter: open, closed, fixed, dismissed, all (default open)")),
		withPagination(),
	)
	r.Add("code_security", false, listAlerts, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		owner, repo, err := ownerRepo(req)
		if err != nil {
			return errResult("list code scanning alerts", err)
		}
		opts := &github.AlertListOptions{
			State:       req.GetString("state", "open"),
			ListOptions: *listOptions(req),
		}
		alerts, _, err := client.CodeScanning.ListAlertsForRepo(ctx, owner, repo, opts)
		if err != nil {
			return errResult("list code scanning alerts", err)
		}
		return jsonResult(alerts)
	})

	getAlert := mcp.NewTool("get_code_scanning_alert",
		mcp.WithDescription("Get a single code scanning alert by ID."),
		withRepoArgs(),
		mcp.WithNumber("alert_id", mcp.Required(), mcp.Description("Code scanning alert ID")),
	)
	r.Add("code_security", false, getAlert, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		owner, repo, err := ownerRepo(req)
		if err != nil {
			return errResult("get code scanning alert", err)
		}
		alertID, errRes := int64Arg(req, "alert_id", "get code scanning alert")
		if errRes != nil {
			return errRes, nil
		}
		alert, _, err := client.CodeScanning.GetAlert(ctx, owner, repo, alertID)
		if err != nil {
			return errResult("get code scanning alert", err)
		}
		return jsonResult(alert)
	})

	updateAlert := mcp.NewTool("update_code_scanning_alert",
		mcp.WithDescription("Update the state of a code scanning alert (dismiss or reopen)."),
		withRepoArgs(),
		mcp.WithNumber("alert_id", mcp.Required(), mcp.Description("Code scanning alert ID")),
		mcp.WithString("state", mcp.Required(), mcp.Description("New state: open, dismissed or fixed")),
	)
	r.Add("code_security", true, updateAlert, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		owner, repo, err := ownerRepo(req)
		if err != nil {
			return errResult("update code scanning alert", err)
		}
		alertID, errRes := int64Arg(req, "alert_id", "update code scanning alert")
		if errRes != nil {
			return errRes, nil
		}
		state, err := req.RequireString("state")
		if err != nil {
			return errResult("update code scanning alert", err)
		}
		alert, _, err := client.CodeScanning.UpdateAlert(ctx, owner, repo, alertID, &github.CodeScanningAlertState{State: state})
		if err != nil {
			return errResult("update code scanning alert", err)
		}
		return jsonResult(alert)
	})
}

// RegisterSecretProtection registers the "secret_protection" toolset
// (secret scanning).
func RegisterSecretProtection(r Registrar, client *github.Client) {
	listAlerts := mcp.NewTool("list_secret_scanning_alerts",
		mcp.WithDescription("List secret scanning alerts of a repository."),
		withRepoArgs(),
		mcp.WithString("state", mcp.Description("Filter: open, resolved, all (default open)")),
		withPagination(),
	)
	r.Add("secret_protection", false, listAlerts, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		owner, repo, err := ownerRepo(req)
		if err != nil {
			return errResult("list secret scanning alerts", err)
		}
		opts := &github.SecretScanningAlertListOptions{
			State:       req.GetString("state", "open"),
			ListOptions: *listOptions(req),
		}
		alerts, _, err := client.SecretScanning.ListAlertsForRepo(ctx, owner, repo, opts)
		if err != nil {
			return errResult("list secret scanning alerts", err)
		}
		return jsonResult(alerts)
	})

	getAlert := mcp.NewTool("get_secret_scanning_alert",
		mcp.WithDescription("Get a single secret scanning alert by number."),
		withRepoArgs(),
		mcp.WithNumber("alert_number", mcp.Required(), mcp.Description("Secret scanning alert number")),
	)
	r.Add("secret_protection", false, getAlert, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		owner, repo, err := ownerRepo(req)
		if err != nil {
			return errResult("get secret scanning alert", err)
		}
		number, errRes := int64Arg(req, "alert_number", "get secret scanning alert")
		if errRes != nil {
			return errRes, nil
		}
		alert, _, err := client.SecretScanning.GetAlert(ctx, owner, repo, number)
		if err != nil {
			return errResult("get secret scanning alert", err)
		}
		return jsonResult(alert)
	})

	listLocations := mcp.NewTool("list_secret_scanning_locations",
		mcp.WithDescription("List where a leaked secret appears inside a repository."),
		withRepoArgs(),
		mcp.WithNumber("alert_number", mcp.Required(), mcp.Description("Secret scanning alert number")),
		withPagination(),
	)
	r.Add("secret_protection", false, listLocations, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		owner, repo, err := ownerRepo(req)
		if err != nil {
			return errResult("list secret scanning locations", err)
		}
		number, errRes := int64Arg(req, "alert_number", "list secret scanning locations")
		if errRes != nil {
			return errRes, nil
		}
		locations, _, err := client.SecretScanning.ListLocationsForAlert(ctx, owner, repo, number, listOptions(req))
		if err != nil {
			return errResult("list secret scanning locations", err)
		}
		return jsonResult(locations)
	})

	updateAlert := mcp.NewTool("update_secret_scanning_alert",
		mcp.WithDescription("Update a secret scanning alert's state. Resolving requires a resolution: "+
			"false_positive, wont_fix, revoked or used_in_tests."),
		withRepoArgs(),
		mcp.WithNumber("alert_number", mcp.Required(), mcp.Description("Secret scanning alert number")),
		mcp.WithString("state", mcp.Required(), mcp.Description("New state: open or resolved")),
		mcp.WithString("resolution", mcp.Description("Required when state=resolved")),
	)
	r.Add("secret_protection", true, updateAlert, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		owner, repo, err := ownerRepo(req)
		if err != nil {
			return errResult("update secret scanning alert", err)
		}
		number, errRes := int64Arg(req, "alert_number", "update secret scanning alert")
		if errRes != nil {
			return errRes, nil
		}
		state, err := req.RequireString("state")
		if err != nil {
			return errResult("update secret scanning alert", err)
		}
		opts := &github.SecretScanningAlertUpdateOptions{State: state}
		if resolution := req.GetString("resolution", ""); resolution != "" {
			opts.Resolution = github.Ptr(resolution)
		}
		alert, _, err := client.SecretScanning.UpdateAlert(ctx, owner, repo, number, opts)
		if err != nil {
			return errResult("update secret scanning alert", err)
		}
		return jsonResult(alert)
	})
}

// RegisterDependabot registers the "dependabot" toolset.
func RegisterDependabot(r Registrar, client *github.Client) {
	listAlerts := mcp.NewTool("list_dependabot_alerts",
		mcp.WithDescription("List Dependabot alerts of a repository."),
		withRepoArgs(),
		mcp.WithString("state", mcp.Description("Filter: open, dismissed, fixed, all (default open)")),
		mcp.WithString("severity", mcp.Description("Filter: low, medium, high, critical")),
		withPagination(),
	)
	r.Add("dependabot", false, listAlerts, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		owner, repo, err := ownerRepo(req)
		if err != nil {
			return errResult("list dependabot alerts", err)
		}
		opts := &github.ListAlertsOptions{
			State:       strPtrIfSet(req.GetString("state", "open")),
			Severity:    strPtrIfSet(req.GetString("severity", "")),
			ListOptions: *listOptions(req),
		}
		alerts, _, err := client.Dependabot.ListRepoAlerts(ctx, owner, repo, opts)
		if err != nil {
			return errResult("list dependabot alerts", err)
		}
		return jsonResult(alerts)
	})

	getAlert := mcp.NewTool("get_dependabot_alert",
		mcp.WithDescription("Get a single Dependabot alert by number."),
		withRepoArgs(),
		mcp.WithNumber("alert_number", mcp.Required(), mcp.Description("Dependabot alert number")),
	)
	r.Add("dependabot", false, getAlert, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		owner, repo, err := ownerRepo(req)
		if err != nil {
			return errResult("get dependabot alert", err)
		}
		number, err := req.RequireInt("alert_number")
		if err != nil {
			return errResult("get dependabot alert", err)
		}
		alert, _, err := client.Dependabot.GetRepoAlert(ctx, owner, repo, number)
		if err != nil {
			return errResult("get dependabot alert", err)
		}
		return jsonResult(alert)
	})

	updateAlert := mcp.NewTool("update_dependabot_alert",
		mcp.WithDescription("Update a Dependabot alert's state. Dismissing requires a dismissed_reason: "+
			"fix_started, inaccurate, no_bandwidth, not_used or tolerable_risk."),
		withRepoArgs(),
		mcp.WithNumber("alert_number", mcp.Required(), mcp.Description("Dependabot alert number")),
		mcp.WithString("state", mcp.Required(), mcp.Description("New state: open, dismissed or fixed")),
		mcp.WithString("dismissed_reason", mcp.Description("Required when state=dismissed")),
		mcp.WithString("dismissed_comment", mcp.Description("Optional comment when dismissing")),
	)
	r.Add("dependabot", true, updateAlert, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		owner, repo, err := ownerRepo(req)
		if err != nil {
			return errResult("update dependabot alert", err)
		}
		number, err := req.RequireInt("alert_number")
		if err != nil {
			return errResult("update dependabot alert", err)
		}
		state, err := req.RequireString("state")
		if err != nil {
			return errResult("update dependabot alert", err)
		}
		stateInfo := &github.DependabotAlertState{State: state}
		if v := req.GetString("dismissed_reason", ""); v != "" {
			stateInfo.DismissedReason = github.Ptr(v)
		}
		if v := req.GetString("dismissed_comment", ""); v != "" {
			stateInfo.DismissedComment = github.Ptr(v)
		}
		alert, _, err := client.Dependabot.UpdateAlert(ctx, owner, repo, number, stateInfo)
		if err != nil {
			return errResult("update dependabot alert", err)
		}
		return jsonResult(alert)
	})
}

// RegisterSecurityAdvisories registers the "security_advisories" toolset.
func RegisterSecurityAdvisories(r Registrar, client *github.Client) {
	listRepoAdvisories := mcp.NewTool("list_repository_security_advisories",
		mcp.WithDescription("List security advisories published for a repository (returns the first batch; cursor pagination not exposed)."),
		withRepoArgs(),
	)
	r.Add("security_advisories", false, listRepoAdvisories, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		owner, repo, err := ownerRepo(req)
		if err != nil {
			return errResult("list repository security advisories", err)
		}
		advisories, _, err := client.SecurityAdvisories.ListRepositorySecurityAdvisories(ctx, owner, repo,
			&github.ListRepositorySecurityAdvisoriesOptions{})
		if err != nil {
			return errResult("list repository security advisories", err)
		}
		return jsonResult(advisories)
	})

	getAdvisory := mcp.NewTool("get_security_advisory",
		mcp.WithDescription("Get a global security advisory by its GHSA ID."),
		mcp.WithString("ghsa_id", mcp.Required(), mcp.Description("GHSA ID, e.g. GHSA-xxxx-xxxx-xxxx")),
	)
	r.Add("security_advisories", false, getAdvisory, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		ghsaID, err := req.RequireString("ghsa_id")
		if err != nil {
			return errResult("get security advisory", err)
		}
		advisory, _, err := client.SecurityAdvisories.GetGlobalSecurityAdvisories(ctx, ghsaID)
		if err != nil {
			return errResult("get security advisory", err)
		}
		return jsonResult(advisory)
	})
}
