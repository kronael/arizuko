package ipc

import (
	"context"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/onvos/arizuko/auth"
)

// registerInspect wires the read-only inspect_* MCP tools.
//
// Tier gating: tier 0 (root) sees all instances; tier ≥1 sees only its
// own folder subtree. This is enforced by isRoot + per-tool checks
// (e.g. rejecting a task that isn't owned by this folder).
func registerInspect(srv *server.MCPServer, db StoreFns, id auth.Identity, folder string) {
	isRoot := id.Tier == 0

	if db.ListRoutes != nil && db.DefaultFolderForJID != nil {
		srv.AddTool(mcp.NewTool("inspect_routing",
			mcp.WithDescription("Inspect routing state: routes visible to this group, JID→folder resolution, and errored-message aggregate per chat. Pass jid to resolve a single JID."),
			mcp.WithString("jid"),
			mcp.WithNumber("limit"),
		), func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			limitVal := req.GetInt("limit", 50)
			if limitVal <= 0 || limitVal > 200 {
				limitVal = 50
			}
			out := map[string]any{}
			if jid := req.GetString("jid", ""); jid != "" {
				if id.Tier > 0 && db.JIDRoutedToFolder != nil && !db.JIDRoutedToFolder(jid, folder) {
					return toolErr("access_denied: jid not routed to your group")
				}
				out["jid"] = jid
				out["resolved_folder"] = db.DefaultFolderForJID(jid)
			}
			routes := db.ListRoutes(folder, isRoot)
			if len(routes) > limitVal {
				routes = routes[:limitVal]
			}
			out["routes"] = routes
			if db.ErroredChats != nil {
				errored := db.ErroredChats(folder, isRoot)
				if len(errored) > limitVal {
					errored = errored[:limitVal]
				}
				out["errored"] = errored
			}
			return toolJSON(out)
		})
	}

	if db.ListTasks != nil {
		srv.AddTool(mcp.NewTool("inspect_tasks",
			mcp.WithDescription("Inspect scheduled tasks visible to this group and recent task_run_logs. Pass task_id for run-log detail."),
			mcp.WithString("task_id"),
			mcp.WithNumber("limit"),
		), func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			limitVal := req.GetInt("limit", 50)
			if limitVal <= 0 || limitVal > 200 {
				limitVal = 50
			}
			tasks := db.ListTasks(folder, isRoot)
			out := map[string]any{"tasks": tasks}
			if tid := req.GetString("task_id", ""); tid != "" && db.TaskRunLogs != nil {
				// Tier-gate: non-root can only read logs for tasks they own.
				if id.Tier > 0 && db.GetTask != nil {
					t, ok := db.GetTask(tid)
					if !ok {
						return toolErr("task not found")
					}
					if err := auth.Authorize(id, "inspect_tasks", auth.AuthzTarget{TaskOwner: t.Owner}); err != nil {
						return toolErr(err.Error())
					}
				}
				out["runs"] = db.TaskRunLogs(tid, limitVal)
			}
			return toolJSON(out)
		})
	}

	if db.GetSession != nil && db.RecentSessions != nil {
		srv.AddTool(mcp.NewTool("inspect_session",
			mcp.WithDescription("Inspect session state for this group: current session_id, recent session_log entries (message count, last error, last context reset)."),
			mcp.WithString("topic"),
			mcp.WithNumber("limit"),
		), func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			limitVal := req.GetInt("limit", 10)
			if limitVal <= 0 || limitVal > 100 {
				limitVal = 10
			}
			topic := req.GetString("topic", "")
			sessID, _ := db.GetSession(folder, topic)
			return toolJSON(map[string]any{
				"folder":     folder,
				"topic":      topic,
				"session_id": sessID,
				"recent":     db.RecentSessions(folder, limitVal),
			})
		})
	}
}
