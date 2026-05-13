package ipc

import (
	"context"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/kronael/arizuko/auth"
)

func registerInspect(srv *server.MCPServer, db StoreFns, id auth.Identity, folder string) {
	isRoot := id.Tier == 0

	if db.ListRoutes != nil && db.DefaultFolderForJID != nil {
		srv.AddTool(mcp.NewTool("inspect_routing",
			mcp.WithDescription("Return routes visible to this group plus the errored-chat aggregate; pass jid to also resolve that JID to its folder. Use when a message isn't reaching the expected group, or to triage delivery failures. Not for per-chat message rows (inspect_messages) or raw route listing without error context (list_routes)."),
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
			mcp.WithDescription("Return tasks visible to this group; pass task_id for that task's recent run logs (durations, errors). Use to debug why a scheduled prompt didn't fire or failed. Not for creating/editing tasks (schedule_task, pause_task, cancel_task)."),
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
			mcp.WithDescription("Return current session_id for this group/topic plus recent session_log rows (message counts, last error, last reset). Use to check whether context is healthy, find the last error, or confirm a reset took. Not for clearing state (reset_session)."),
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

	if db.GetIdentityForSub != nil {
		srv.AddTool(mcp.NewTool("inspect_identity",
			mcp.WithDescription("Resolve a platform sender sub (e.g. tg:123, discord:abc) to its canonical identity and the full set of subs claimed by that identity. Use to recognize a user across channels (\"is the WhatsApp tg:42 the same person as the Discord user discord:7?\"). Advisory only — agents query, never enforce. Returns {identity:null, subs:[]} when the sub is unclaimed."),
			mcp.WithString("sub", mcp.Required()),
		), func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			sub := req.GetString("sub", "")
			if sub == "" {
				return toolErr("sub required")
			}
			idn, subs, ok := db.GetIdentityForSub(sub)
			if !ok {
				return toolJSON(map[string]any{"sub": sub, "identity": nil, "subs": []string{}})
			}
			return toolJSON(map[string]any{"sub": sub, "identity": idn, "subs": subs})
		})
	}
}
