package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/kronael/arizuko/chanlib"
	"github.com/kronael/arizuko/core"
)

// handleSend is routd's callback to push assistant messages to SSE subscribers.
// routd is the sole message appender — we only publish to hub, no local PutMessage.
func (s *server) handleSend(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ChatJID string `json:"chat_jid"`
		Content string `json:"content"`
		ReplyTo string `json:"reply_to"`
		TurnID  string `json:"turn_id"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxJSONBody)
	if json.NewDecoder(r.Body).Decode(&req) != nil || req.ChatJID == "" || req.Content == "" {
		chanlib.WriteErr(w, 400, "chat_jid and content required")
		return
	}

	folder := strings.TrimPrefix(req.ChatJID, "web:")
	// messages lives in routd.db (stRoutd) — split ownership
	topic := s.stRoutd.TopicByMessageID(req.ReplyTo, req.ChatJID)

	id := core.MsgID("bot")
	ts := time.Now()

	payload, _ := json.Marshal(map[string]any{
		"id":         id,
		"role":       "assistant",
		"content":    req.Content,
		"created_at": ts.Format(time.RFC3339),
		"turn_id":    req.TurnID,
	})
	s.hub.publish(folder, topic, "message", string(payload))

	chanlib.WriteJSON(w, map[string]any{"ok": true, "id": id})
}

func (s *server) handleRoundDone(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Folder string `json:"folder"`
		TurnID string `json:"turn_id"`
		Status string `json:"status"`
		Error  string `json:"error"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxJSONBody)
	if json.NewDecoder(r.Body).Decode(&req) != nil || req.TurnID == "" {
		chanlib.WriteErr(w, 400, "turn_id required")
		return
	}
	jid := "web:" + req.Folder
	// messages lives in routd.db (stRoutd) — split ownership
	topic := s.stRoutd.TopicByMessageID(req.TurnID, jid)
	payload, _ := json.Marshal(map[string]any{
		"turn_id": req.TurnID,
		"status":  req.Status,
		"error":   req.Error,
	})
	s.hub.publish(req.Folder, topic, "round_done", string(payload))
	chanlib.WriteJSON(w, map[string]any{"ok": true})
}

func (s *server) handleTyping(w http.ResponseWriter, _ *http.Request) {
	chanlib.WriteJSON(w, map[string]any{"ok": true})
}
