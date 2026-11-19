package tools

import (
	"context"
	"time"

	"github.com/google/go-github/v72/github"
	mcp "github.com/mark3labs/mcp-go/mcp"
)

// RegisterNotifications registers the "notifications" toolset for the
// authenticated account.
func RegisterNotifications(r Registrar, client *github.Client) {
	listNotifications := mcp.NewTool("list_notifications",
		mcp.WithDescription("List notifications of the authenticated user."),
		mcp.WithBoolean("all", mcp.Description("Include already-read notifications")),
		mcp.WithBoolean("participating", mcp.Description("Only notifications the user is directly participating in")),
		mcp.WithString("since", mcp.Description("Only notifications updated at or after this RFC3339 timestamp")),
		withPagination(),
	)
	r.Add("notifications", false, listNotifications, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		opts := &github.NotificationListOptions{
			All:           req.GetBool("all", false),
			Participating: req.GetBool("participating", false),
			ListOptions:   *listOptions(req),
		}
		if v := req.GetString("since", ""); v != "" {
			t, err := time.Parse(time.RFC3339, v)
			if err != nil {
				return errResult("list notifications (since must be RFC3339)", err)
			}
			opts.Since = t
		}
		notifications, resp, err := client.Activity.ListNotifications(ctx, opts)
		if err != nil {
			return errResult("list notifications", err)
		}
		return jsonResultWithPage(resp, notifications)
	})

	getThread := mcp.NewTool("get_notification_thread",
		mcp.WithDescription("Get a single notification thread by ID."),
		mcp.WithString("thread_id", mcp.Required(), mcp.Description("Notification thread ID")),
	)
	r.Add("notifications", false, getThread, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		threadID, err := req.RequireString("thread_id")
		if err != nil {
			return errResult("get notification thread", err)
		}
		thread, _, err := client.Activity.GetThread(ctx, threadID)
		if err != nil {
			return errResult("get notification thread", err)
		}
		return jsonResult(thread)
	})

	markThreadRead := mcp.NewTool("mark_thread_read",
		mcp.WithDescription("Mark a notification thread as read."),
		mcp.WithString("thread_id", mcp.Required(), mcp.Description("Notification thread ID")),
	)
	r.Add("notifications", true, markThreadRead, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		threadID, err := req.RequireString("thread_id")
		if err != nil {
			return errResult("mark thread read", err)
		}
		if _, err := client.Activity.MarkThreadRead(ctx, threadID); err != nil {
			return errResult("mark thread read", err)
		}
		return jsonResult(map[string]any{"marked_read": threadID})
	})
}
