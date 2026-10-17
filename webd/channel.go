package main

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/kronael/arizuko/chanlib"
	"github.com/kronael/arizuko/core"
)

// webCaps is what webd registers: it posts messages and replaces a frame it
// posted. `typing` is absent until webd renders presence (specs/6/46).
var webCaps = map[string]bool{"send_text": true, "edit": true}

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

// handleEdit is routd's edit verb: it publishes a `message` frame carrying the
// id of the frame being edited and the new content, so a browser that holds
// that id replaces it and one that does not appends it. routd stores the new
// content on the row itself; webd keeps nothing.
//
// The hub key is the chat JID from the wire, verbatim, and the topic and turn
// come from the row the target id names, so the frame reaches exactly the
// subscribers the original frame reached (BUGS L30). The kind comes from the
// row's verb (frameKind), not from the text.
func (s *server) handleEdit(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ChatJID  string `json:"chat_jid"`
		TargetID string `json:"target_id"`
		Content  string `json:"content"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxJSONBody)
	if json.NewDecoder(r.Body).Decode(&req) != nil || req.ChatJID == "" || req.TargetID == "" || req.Content == "" {
		chanlib.WriteErr(w, 400, "chat_jid, target_id and content required")
		return
	}
	// messages lives in routd.db (stRoutd) — split ownership. The lookup is
	// bound to the chat, so a target id in another chat does not resolve.
	m, ok := s.stRoutd.MessageByID(req.ChatJID, req.TargetID)
	if !ok {
		chanlib.WriteErr(w, 404, "message not found")
		return
	}
	if !m.BotMsg {
		chanlib.WriteErr(w, 403, "only assistant messages can be edited")
		return
	}
	payload, _ := json.Marshal(map[string]any{
		"id":         req.TargetID,
		"role":       "assistant",
		"content":    req.Content,
		"created_at": m.Timestamp.Format(time.RFC3339),
		"turn_id":    m.TurnID,
		"kind":       frameKind(m),
	})
	s.hub.publish(req.ChatJID, m.Topic, "message", string(payload))
	chanlib.WriteJSON(w, map[string]any{"ok": true})
}

// handleTyping answers ok so an older routd or client that still posts it is
// not refused. webd renders no presence and does not advertise `typing`.
func (s *server) handleTyping(w http.ResponseWriter, _ *http.Request) {
	chanlib.WriteJSON(w, map[string]any{"ok": true})
}
