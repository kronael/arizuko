package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/onvos/arizuko/chanlib"
	"github.com/onvos/arizuko/core"
)

// handleSend receives a bot message from gated and writes it to store + SSE hub.
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
	topic := s.st.TopicByMessageID(req.ReplyTo, req.ChatJID)

	id := core.MsgID("bot")
	m := core.Message{
		ID:        id,
		ChatJID:   req.ChatJID,
		Sender:    s.cfg.assistantName,
		Content:   req.Content,
		Timestamp: time.Now(),
		BotMsg:    true,
		ReplyToID: req.ReplyTo,
		Topic:     topic,
		TurnID:    req.TurnID,
	}
	if err := s.st.PutMessage(m); err != nil {
		chanlib.WriteErr(w, 500, "store failed")
		return
	}

	payload, _ := json.Marshal(map[string]any{
		"id":         m.ID,
		"role":       "assistant",
		"content":    m.Content,
		"created_at": m.Timestamp.Format(time.RFC3339),
		"turn_id":    req.TurnID,
	})
	s.hub.publish(folder, topic, "message", string(payload))

	chanlib.WriteJSON(w, map[string]any{"ok": true, "id": id})
}

// handleRoundDone is gated→webd notification that the agent finished a run.
// Looks up the turn's chat topic and emits a terminal round_done SSE frame
// on (folder, topic) so any /turn/<id>/sse subscribers can disconnect.
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
	topic := s.st.TopicByMessageID(req.TurnID, jid)
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
