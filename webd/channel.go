package main

import (
	"encoding/json"
	"net/http"
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
	s.hub.publish(req.ChatJID, topic, "message", string(payload))

	chanlib.WriteJSON(w, map[string]any{"ok": true, "id": id})
}

// handleRoundDone publishes routd's turn-closed notice. The wire carries the
// chat JID verbatim (never a derived folder — a "web:"+folder rebuild broke
// every hook: chat, BUGS L30), so topic lookup and hub key both use it as-is.
func (s *server) handleRoundDone(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ChatJID string `json:"chat_jid"`
		TurnID  string `json:"turn_id"`
		Status  string `json:"status"`
		Error   string `json:"error"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxJSONBody)
	if json.NewDecoder(r.Body).Decode(&req) != nil || req.TurnID == "" || req.ChatJID == "" {
		chanlib.WriteErr(w, 400, "turn_id and chat_jid required")
		return
	}
	// messages lives in routd.db (stRoutd) — split ownership
	topic := s.stRoutd.TopicByMessageID(req.TurnID, req.ChatJID)
	payload, _ := json.Marshal(map[string]any{
		"turn_id": req.TurnID,
		"status":  req.Status,
		"error":   req.Error,
	})
	s.hub.publish(req.ChatJID, topic, "round_done", string(payload))
	chanlib.WriteJSON(w, map[string]any{"ok": true})
}

func (s *server) handleTyping(w http.ResponseWriter, _ *http.Request) {
	chanlib.WriteJSON(w, map[string]any{"ok": true})
}
