package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/onvos/arizuko/chanlib"
	"github.com/onvos/arizuko/core"
)

// handleMCP: streamable-HTTP MCP, one endpoint per instance. Tools take
// `folder` and check grants; outbound identity is the authed user (never anon).
func (s *server) handleMCP(w http.ResponseWriter, r *http.Request) {
	if r.Body != nil {
		r.Body = http.MaxBytesReader(w, r.Body, maxJSONBody)
	}
	sub := userSub(r)
	name := userName(r)
	if name == "" {
		name = sub
	}
	mcpSrv := s.buildWebMCP(sub, name, userGroups(r))
	h := mcpserver.NewStreamableHTTPServer(mcpSrv, mcpserver.WithStateLess(true))
	h.ServeHTTP(w, r)
}

func (s *server) buildWebMCP(sub, name string, grants []string) *mcpserver.MCPServer {
	srv := mcpserver.NewMCPServer("arizuko-web", "1.0")

	resolveGroup := func(folder string) (core.Group, *mcp.CallToolResult) {
		folder = strings.TrimSpace(folder)
		if folder == "" {
			return core.Group{}, mcp.NewToolResultError("folder required")
		}
		if !userAllowedFolder(grants, folder) {
			return core.Group{}, mcp.NewToolResultError("forbidden: no grant for folder")
		}
		g, ok := s.st.GroupByFolder(folder)
		if !ok {
			return core.Group{}, mcp.NewToolResultError("group not found")
		}
		return g, nil
	}

	srv.AddTool(mcp.NewTool("list_groups",
		mcp.WithDescription("List groups (folders) this user has access to."),
	), func(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		all := s.st.AllGroups()
		type row struct {
			Folder string `json:"folder"`
			Name   string `json:"name"`
			Parent string `json:"parent,omitempty"`
		}
		out := make([]row, 0, len(all))
		for _, g := range all {
			if !userAllowedFolder(grants, g.Folder) {
				continue
			}
			out = append(out, row{g.Folder, g.Name, g.Parent})
		}
		data, _ := json.Marshal(map[string]any{"groups": out})
		return mcp.NewToolResultText(string(data)), nil
	})

	srv.AddTool(mcp.NewTool("send",
		mcp.WithDescription("Send a message to a group. Returns the message id + topic."),
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
			topic = fmt.Sprintf("t%d", time.Now().UnixMilli())
		}

		id := core.MsgID("msg")
		m := core.Message{
			ID:        id,
			ChatJID:   "web:" + g.Folder,
			Sender:    sub,
			Name:      name,
			Content:   content,
			Timestamp: time.Now(),
			Topic:     topic,
		}
		if err := s.st.PutMessage(m); err != nil {
			return mcp.NewToolResultError("store failed: " + err.Error()), nil
		}
		if err := s.rc.SendMessage(chanlib.InboundMsg{
			ID:         m.ID,
			ChatJID:    m.ChatJID,
			Sender:     sub,
			SenderName: name,
			Content:    content,
			Timestamp:  m.Timestamp.Unix(),
			// Web MCP: each call carries one authenticated sub. Single-user.
			IsGroup: false,
		}); err != nil {
			return mcp.NewToolResultError("router: " + err.Error()), nil
		}
		payload, _ := json.Marshal(map[string]any{
			"id":         m.ID,
			"role":       "user",
			"content":    m.Content,
			"sender":     name,
			"created_at": m.Timestamp.Format(time.RFC3339),
		})
		s.hub.publish(g.Folder, topic, "message", string(payload))
		out, _ := json.Marshal(map[string]any{"id": m.ID, "topic": topic, "folder": g.Folder})
		return mcp.NewToolResultText(string(out)), nil
	})

	srv.AddTool(mcp.NewTool("get_history",
		mcp.WithDescription("Fetch recent messages on a topic in a group. limit ≤ 100."),
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
		msgs, err := s.st.MessagesByTopic(g.Folder, topic, time.Now(), limit)
		if err != nil {
			return mcp.NewToolResultError("query failed: " + err.Error()), nil
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
