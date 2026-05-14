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

	"github.com/kronael/arizuko/chanlib"
	"github.com/kronael/arizuko/core"
)

// slinkMCPWaitCap caps get_round blocking so forgotten clients can't pin goroutines.
const slinkMCPWaitCap = 5 * time.Minute

// POST /slink/<token>/mcp — 2-tool MCP surface (send_message, get_round).
// Token possession = group membership.
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

func (s *server) buildSlinkMCP(g core.Group, token string) *mcpserver.MCPServer {
	srv := mcpserver.NewMCPServer("arizuko-slink", "1.0")

	sender := "anon:slink-" + chanlib.ShortHash(token)
	senderName := "slink"

	jid := "web:" + g.Folder

	srv.AddTool(mcp.NewTool("send_message",
		mcp.WithDescription("Submit a message to the group. Returns {turn_id} — the originating message id, used as the round handle by get_round. Optional topic groups multiple rounds into the same conversation thread; reuse it on later calls to keep the agent in the same context (auto-generated when omitted)."),
		mcp.WithString("content", mcp.Required()),
		mcp.WithString("topic", mcp.Description("Optional conversation thread id. Auto-generated if omitted.")),
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

	srv.AddTool(mcp.NewTool("get_round",
		mcp.WithDescription("Read assistant frames for a round (mirrors GET /slink/<token>/<turn_id>). turn_id is the round handle from send_message. Optional after=<msg_id> cursor-pages forward. With wait=true, blocks until at least one new assistant frame arrives or the ~5 min deadline. Returns {turn_id, status, frames, last_frame_id, done}."),
		mcp.WithString("turn_id", mcp.Required(), mcp.Description("Round handle from send_message (originating message id).")),
		mcp.WithString("after", mcp.Description("Cursor: return only frames after this message id.")),
		mcp.WithBoolean("wait"),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		turnID := strings.TrimSpace(req.GetString("turn_id", ""))
		if turnID == "" {
			return mcp.NewToolResultError("turn_id required"), nil
		}
		after := strings.TrimSpace(req.GetString("after", ""))
		wait := req.GetBool("wait", false)

		frames := s.collectRoundFrames(turnID, after)
		info, _ := s.st.GetTurnResult(g.Folder, turnID)
		done := hasAssistant(frames)
		if !wait || done {
			return roundSnapshot(turnID, info.Status, frames, done), nil
		}

		topic := s.st.TopicByMessageID(turnID, jid)
		if topic == "" {
			return roundSnapshot(turnID, info.Status, frames, false), nil
		}
		ch, unsub := s.hub.subscribe(g.Folder, topic)
		defer unsub()

		frames = s.collectRoundFrames(turnID, after)
		if hasAssistant(frames) {
			info, _ = s.st.GetTurnResult(g.Folder, turnID)
			return roundSnapshot(turnID, info.Status, frames, true), nil
		}

		deadline, cancel := context.WithTimeout(ctx, slinkMCPWaitCap)
		defer cancel()
		if a, ok := waitForAssistant(deadline, ch); ok {
			frames = appendUnique(frames, a)
			info, _ = s.st.GetTurnResult(g.Folder, turnID)
			return roundSnapshot(turnID, info.Status, frames, true), nil
		}
		info, _ = s.st.GetTurnResult(g.Folder, turnID)
		return roundSnapshot(turnID, info.Status, frames, false), nil
	})

	srv.AddTool(mcp.NewTool("get_round_status",
		mcp.WithDescription("Cheap status check for a round (mirrors GET /slink/<token>/<turn_id>/status). No frame payload — just status, frames_count, last_frame_id."),
		mcp.WithString("turn_id", mcp.Required(), mcp.Description("Round handle from send_message.")),
	), func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		turnID := strings.TrimSpace(req.GetString("turn_id", ""))
		if turnID == "" {
			return mcp.NewToolResultError("turn_id required"), nil
		}
		info, _ := s.st.GetTurnResult(g.Folder, turnID)
		msgs, err := s.st.TurnFrames(turnID, "", 200)
		if err != nil {
			return mcp.NewToolResultError("query failed"), nil
		}
		last := ""
		if n := len(msgs); n > 0 {
			last = msgs[n-1].ID
		}
		data, _ := json.Marshal(map[string]any{
			"turn_id":       turnID,
			"status":        info.Status,
			"frames_count":  len(msgs),
			"last_frame_id": last,
		})
		return mcp.NewToolResultText(string(data)), nil
	})

	return srv
}

func (s *server) collectRoundFrames(turnID, after string) []map[string]any {
	msgs, err := s.st.TurnFrames(turnID, after, 100)
	if err != nil {
		slog.Warn("slink-mcp get_round query", "turn_id", turnID, "err", err)
		return nil
	}
	out := make([]map[string]any, 0, len(msgs))
	for _, m := range msgs {
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

func hasAssistant(frames []map[string]any) bool {
	for _, f := range frames {
		if r, _ := f["role"].(string); r == "assistant" {
			return true
		}
	}
	return false
}

// appendUnique deduplicates by id — hub frames can race the DB read on get_round wait.
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

// roundSnapshot mirrors the GET /slink/<token>/<turn_id> response shape.
func roundSnapshot(turnID, status string, frames []map[string]any, done bool) *mcp.CallToolResult {
	last := ""
	if n := len(frames); n > 0 {
		if id, _ := frames[n-1]["id"].(string); id != "" {
			last = id
		}
	}
	data, _ := json.Marshal(map[string]any{
		"turn_id":       turnID,
		"status":        status,
		"frames":        frames,
		"last_frame_id": last,
		"done":          done,
	})
	return mcp.NewToolResultText(string(data))
}
