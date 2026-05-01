package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/onvos/arizuko/chanlib"
	"github.com/onvos/arizuko/core"
)

// slinkMCPWaitCap bounds the get_round wait blocking deadline. External
// agents pass `wait: true` and we cap server-side so a forgotten client
// can't pin a goroutine forever.
const slinkMCPWaitCap = 5 * time.Minute

// handleSlinkMCP exposes a 3-tool MCP transport scoped to one slink
// token's group: send_message, steer, get_round. The token IS the auth
// — possessing it = group membership. POST /slink/<token>/mcp.
func (s *server) handleSlinkMCP(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	g, ok := s.st.GroupBySlinkToken(token)
	if !ok {
		slog.Warn("slink-mcp token not found", "token_hash", chanlib.ShortHash(token))
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if r.Body != nil {
		r.Body = http.MaxBytesReader(w, r.Body, maxJSONBody)
	}
	mcpSrv := s.buildSlinkMCP(g, token)
	h := mcpserver.NewStreamableHTTPServer(mcpSrv, mcpserver.WithStateLess(true))
	h.ServeHTTP(w, r)
}

// buildSlinkMCP wires the 3-tool surface against one group. Sender
// identity is anon-derived (slink is token-only by design).
func (s *server) buildSlinkMCP(g core.Group, token string) *mcpserver.MCPServer {
	srv := mcpserver.NewMCPServer("arizuko-slink", "1.0")

	sender := "anon:slink-" + chanlib.ShortHash(token)
	senderName := "slink"

	srv.AddTool(mcp.NewTool("send_message",
		mcp.WithDescription("Send a fresh user message to the group; starts a new round. Returns {turn_id, topic} where turn_id is the originating message id. Use steer to extend the same round; use get_round to read assistant replies."),
		mcp.WithString("content", mcp.Required()),
		mcp.WithString("topic", mcp.Description("Optional round identifier. Auto-generated if omitted.")),
	), func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		content := strings.TrimSpace(req.GetString("content", ""))
		if content == "" {
			return mcp.NewToolResultError("content required"), nil
		}
		topic := strings.TrimSpace(req.GetString("topic", ""))
		if topic == "" {
			topic = fmt.Sprintf("t%d", time.Now().UnixMilli())
		}
		if len(topic) > maxTopicLen {
			return mcp.NewToolResultError("topic too long"), nil
		}
		m, _, err := s.injectSlink(g, content, topic, sender, senderName, token)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		out, _ := json.Marshal(map[string]any{
			"turn_id": m.ID,
			"topic":   topic,
			"folder":  g.Folder,
		})
		return mcp.NewToolResultText(string(out)), nil
	})

	srv.AddTool(mcp.NewTool("steer",
		mcp.WithDescription("Extend an existing round with a follow-up user message on the same topic. turn_id is the topic returned by send_message. Use for clarifications mid-round; use send_message to start a fresh round."),
		mcp.WithString("turn_id", mcp.Required(), mcp.Description("Topic id from send_message.")),
		mcp.WithString("content", mcp.Required()),
	), func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		topic := strings.TrimSpace(req.GetString("turn_id", ""))
		content := strings.TrimSpace(req.GetString("content", ""))
		if topic == "" {
			return mcp.NewToolResultError("turn_id required"), nil
		}
		if content == "" {
			return mcp.NewToolResultError("content required"), nil
		}
		if len(topic) > maxTopicLen {
			return mcp.NewToolResultError("turn_id too long"), nil
		}
		m, _, err := s.injectSlink(g, content, topic, sender, senderName, token)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		out, _ := json.Marshal(map[string]any{
			"turn_id": m.ID,
			"topic":   topic,
			"folder":  g.Folder,
		})
		return mcp.NewToolResultText(string(out)), nil
	})

	srv.AddTool(mcp.NewTool("get_round",
		mcp.WithDescription("Read assistant frames for a round (topic). Without wait, returns whatever frames are stored now. With wait=true, blocks until at least one new assistant frame arrives or the server-side deadline hits (~5 min). Returns {frames, done} — done=true when an assistant reply was observed."),
		mcp.WithString("turn_id", mcp.Required(), mcp.Description("Topic id from send_message.")),
		mcp.WithBoolean("wait"),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		topic := strings.TrimSpace(req.GetString("turn_id", ""))
		if topic == "" {
			return mcp.NewToolResultError("turn_id required"), nil
		}
		if len(topic) > maxTopicLen {
			return mcp.NewToolResultError("turn_id too long"), nil
		}
		wait := req.GetBool("wait", false)

		frames := s.collectRoundFrames(g.Folder, topic)
		done := hasAssistant(frames)
		if !wait || done {
			return roundResult(frames, done), nil
		}

		ch, unsub := s.hub.subscribe(g.Folder, topic)
		defer unsub()

		// Re-poll after subscribing to avoid missing a frame written between
		// the first read and the subscribe call.
		frames = s.collectRoundFrames(g.Folder, topic)
		if hasAssistant(frames) {
			return roundResult(frames, true), nil
		}

		deadline, cancel := context.WithTimeout(ctx, slinkMCPWaitCap)
		defer cancel()
		if a, ok := waitForAssistant(deadline, ch); ok {
			frames = appendUnique(frames, a)
			return roundResult(frames, true), nil
		}
		return roundResult(frames, false), nil
	})

	return srv
}

// collectRoundFrames returns the messages on (folder, topic) ordered
// oldest-first, shaped as the slink frame payload.
func (s *server) collectRoundFrames(folder, topic string) []map[string]any {
	msgs, err := s.st.MessagesByTopic(folder, topic, time.Now().Add(time.Hour), 100)
	if err != nil {
		slog.Warn("slink-mcp get_round query", "folder", folder, "topic", topic, "err", err)
		return nil
	}
	out := make([]map[string]any, 0, len(msgs))
	for i := len(msgs) - 1; i >= 0; i-- {
		m := msgs[i]
		out = append(out, map[string]any{
			"id":         m.ID,
			"role":       messageRole(m),
			"content":    m.Content,
			"sender":     m.Name,
			"created_at": m.Timestamp.Format(time.RFC3339),
		})
	}
	return out
}

// hasAssistant reports whether the frame list contains at least one
// assistant-role frame (used as the round's "done" signal).
func hasAssistant(frames []map[string]any) bool {
	for _, f := range frames {
		if r, _ := f["role"].(string); r == "assistant" {
			return true
		}
	}
	return false
}

// appendUnique adds frame to frames if its id isn't already present.
// Hub frames can race the DB read on get_round wait → dedupe by id.
func appendUnique(frames []map[string]any, frame map[string]any) []map[string]any {
	id, _ := frame["id"].(string)
	if id != "" {
		for _, f := range frames {
			if existing, _ := f["id"].(string); existing == id {
				return frames
			}
		}
	}
	return append(frames, frame)
}

func roundResult(frames []map[string]any, done bool) *mcp.CallToolResult {
	data, _ := json.Marshal(map[string]any{
		"frames": frames,
		"done":   done,
	})
	return mcp.NewToolResultText(string(data))
}
