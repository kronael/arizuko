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
	}
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
	})
	s.hub.publish(folder, topic, "message", string(payload))

	chanlib.WriteJSON(w, map[string]any{"ok": true, "id": id})
}

func (s *server) handleTyping(w http.ResponseWriter, _ *http.Request) {
	chanlib.WriteJSON(w, map[string]any{"ok": true})
}
