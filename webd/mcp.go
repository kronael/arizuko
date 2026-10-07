package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/chanlib"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/groupfolder"
)

// chatPin fixes the ONE chat an anonymous route-token principal addresses.
//
// The token row RECORDS its JID and its link context (spec 5/W); send and
// get_history READ them. Neither may rebuild the JID from the folder: a token's
// JID can be suffixed (`web:atlas/sfx`) or a hook (`hook:atlas/github`), and
// `"web:"+folder` then names a different chat than the one the token delivers to.
// Nil for an identity caller, whose N folders each address their own `web:<folder>`.
type chatPin struct {
	jid     string
	context string
}

// POST /mcp — identity surface. proxyd stamps X-User-*; the grant set is the
// caller's N folders.
func (s *server) handleMCP(w http.ResponseWriter, r *http.Request) {
	if r.Body != nil {
		r.Body = http.MaxBytesReader(w, r.Body, maxJSONBody)
	}
	sub := userSub(r)
	name := userName(r)
	if name == "" {
		name = sub
	}
	mcpSrv := s.buildWebMCP(sub, name, userGroups(r), nil)
	h := mcpserver.NewStreamableHTTPServer(mcpSrv, mcpserver.WithStateLess(true))
	h.ServeHTTP(w, r)
}

// POST /chat/<token>/mcp — the SAME tool surface as POST /mcp (BUGS L139).
//
// A route token authenticates an ANONYMOUS principal whose destination set has
// exactly one element. The vocabulary does not change with the door: list_groups
// returns the one pinned group, send and get_history address it.
//
// The gate is neither `identified` nor `transited`. `identified` demands an
// X-User-Sub that an anonymous visitor cannot have, so requiring it would 403 the
// whole anonymous product; `transited` guards long-lived subscriptions (the SSE
// stream pins a goroutine) and this surface is stateless, like its fire-and-forget
// `POST /chat/{token}` sibling. Token possession IS the credential, and
// lookupRouteToken has already refused a token naming a deleted folder.
//
// Containment: the grant set is built from the ROW, never from a request param, so
// auth.MatchGroups can only ever admit row.Folder(). Its glob is segment-wise, so
// `atlas` admits `atlas` alone — a token never reaches a subfolder (5/33-paths-roles dec 8).
func (s *server) handleChatTokenMCP(w http.ResponseWriter, r *http.Request) {
	row, ok := s.lookupRouteToken(w, r, r.PathValue("token"))
	if !ok {
		return
	}
	if r.Body != nil {
		r.Body = http.MaxBytesReader(w, r.Body, maxJSONBody)
	}
	// Per-TOKEN, not per-JID: two links onto one chat stay distinguishable in the
	// transcript, and this is the sender value the surface already wrote.
	sub := "anon:chat-" + chanlib.ShortHash(r.PathValue("token"))
	mcpSrv := s.buildWebMCP(sub, "chat", []string{row.Folder()},
		&chatPin{jid: row.JID, context: row.Context})
	h := mcpserver.NewStreamableHTTPServer(mcpSrv, mcpserver.WithStateLess(true))
	h.ServeHTTP(w, r)
}

// buildWebMCP renders the one web tool surface. Principal, grants and pin are plain
// arguments, so the builder does not care whether they came from proxyd's headers
// or from a route-token row — the doors differ only in the SIZE of the destination
// set: N folders for an identity caller, exactly 1 for a token (BUGS L139).
func (s *server) buildWebMCP(sub, name string, grants []string, pin *chatPin) *mcpserver.MCPServer {
	srv := mcpserver.NewMCPServer("arizuko-web", "1.0")

	// Operator-only resource tools (spec 5/17-openapi-mcp). Invisible to non-operators, so a
	// one-element grant set never sees them.
	registerRoutesMCP(srv, s.proxyd, sub, name, grants)

	// chatJID answers "which chat does this folder mean for THIS principal": read
	// off the token row when pinned, constructed when the caller has an identity.
	chatJID := func(folder string) string {
		if pin != nil {
			return pin.jid
		}
		return groupfolder.WebJID(folder)
	}
	linkContext := ""
	if pin != nil {
		linkContext = pin.context
	}

	// groups lives in routd.db (stRoutd) — split ownership
	resolveGroup := func(folder string) (core.Group, *mcp.CallToolResult) {
		folder = strings.TrimSpace(folder)
		if folder == "" {
			return core.Group{}, mcp.NewToolResultError("folder required")
		}
		if !auth.MatchGroups(grants, folder) {
			return core.Group{}, mcp.NewToolResultError("forbidden: no grant for folder")
		}
		g, ok := s.stRoutd.GroupByFolder(folder)
		if !ok {
			return core.Group{}, mcp.NewToolResultError("group not found")
		}
		return g, nil
	}

	srv.AddTool(mcp.NewTool("list_groups",
		mcp.WithDescription("List groups (folders) this caller may reach. A route-token caller sees exactly one — the group its link is pinned to."),
	), func(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		all := s.stRoutd.AllGroups()
		type row struct {
			Folder string `json:"folder"`
		}
		out := make([]row, 0, len(all))
		for _, g := range all {
			if !auth.MatchGroups(grants, g.Folder) {
				continue
			}
			out = append(out, row{g.Folder})
		}
		data, _ := json.Marshal(map[string]any{"groups": out})
		return mcp.NewToolResultText(string(data)), nil
	})

	// routd is the sole message appender — no local PutMessage. injectRouteMessage
	// is the one injector both doors use; it delivers via rc.SendMessage and
	// publishes the hub frame.
	srv.AddTool(mcp.NewTool("send",
		mcp.WithDescription("Send a message to a group. Returns the message id + topic; the id is the round handle. Reuse topic to stay in one conversation thread."),
		mcp.WithString("folder", mcp.Required()),
		mcp.WithString("content", mcp.Required()),
		mcp.WithString("topic"),
	), func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		g, errRes := resolveGroup(req.GetString("folder", ""))
		if errRes != nil {
			return errRes, nil
		}
		content := strings.TrimSpace(req.GetString("content", ""))
		if content == "" {
			return mcp.NewToolResultError("content required"), nil
		}
		topic := strings.TrimSpace(req.GetString("topic", ""))
		if topic == "" {
			topic = newTopicID()
		}
		if len(topic) > maxTopicLen {
			return mcp.NewToolResultError("topic too long"), nil
		}
		m, _, err := s.injectRouteMessage(
			chatJID(g.Folder), g.Folder, content, topic, sub, name, "", linkContext)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		out, _ := json.Marshal(map[string]any{"id": m.ID, "topic": topic, "folder": g.Folder})
		return mcp.NewToolResultText(string(out)), nil
	})

	srv.AddTool(mcp.NewTool("get_history",
		mcp.WithDescription("Fetch recent messages on a topic in a group, newest first. limit ≤ 100."),
		mcp.WithString("folder", mcp.Required()),
		mcp.WithString("topic", mcp.Required()),
		mcp.WithNumber("limit"),
	), func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		g, errRes := resolveGroup(req.GetString("folder", ""))
		if errRes != nil {
			return errRes, nil
		}
		topic := strings.TrimSpace(req.GetString("topic", ""))
		if topic == "" {
			return mcp.NewToolResultError("topic required"), nil
		}
		limit := req.GetInt("limit", 50)
		if limit <= 0 || limit > 100 {
			limit = 50
		}
		// messages lives in routd.db (stRoutd) — split ownership. Reads the same
		// chat send wrote to, so a pinned token reads back its own suffixed JID.
		msgs, err := s.stRoutd.MessagesByThread(chatJID(g.Folder), topic, time.Now(), limit)
		if err != nil {
			return mcp.NewToolResultErrorf("query failed: %v", err), nil
		}
		type msgOut struct {
			ID        string    `json:"id"`
			Role      string    `json:"role"`
			Content   string    `json:"content"`
			Sender    string    `json:"sender"`
			CreatedAt time.Time `json:"created_at"`
		}
		out := make([]msgOut, len(msgs))
		for i, m := range msgs {
			out[i] = msgOut{m.ID, messageRole(m), m.Content, m.Name, m.Timestamp}
		}
		data, _ := json.Marshal(map[string]any{"messages": out, "count": len(out), "folder": g.Folder})
		return mcp.NewToolResultText(string(data)), nil
	})

	return srv
}
