package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/onvos/arizuko/chanlib"
	"github.com/onvos/arizuko/core"
)

// anonSender derives a stable, privacy-preserving sender ID from client IP.
// Format: "anon:<8-char hex prefix of sha256(ip)>"
func anonSender(r *http.Request) string {
	ip := r.Header.Get("X-Forwarded-For")
	if ip == "" {
		ip, _, _ = net.SplitHostPort(r.RemoteAddr)
	}
	if ip == "" {
		ip = "unknown"
	}
	sum := sha256.Sum256([]byte(ip))
	return fmt.Sprintf("anon:%x", sum[:4])
}

// handleSlinkPost accepts user messages via token-authenticated POST.
// POST /slink/<token>   body: content=Hello&topic=abc123
// With Accept: text/event-stream the connection is upgraded to SSE and
// held open — the caller receives the user's own bubble plus any
// subsequent assistant responses on the same (folder, topic) stream.
func (s *server) handleSlinkPost(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	g, ok := s.st.GroupBySlinkToken(token)
	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	if r.ParseForm() != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	content := strings.TrimSpace(r.FormValue("content"))
	topic := strings.TrimSpace(r.FormValue("topic"))
	if content == "" {
		http.Error(w, "content required", http.StatusBadRequest)
		return
	}
	if topic == "" {
		topic = fmt.Sprintf("t%d", time.Now().UnixMilli())
	}

	sender := userSub(r)
	senderName := userName(r)
	if sender == "" {
		sender = anonSender(r)
		senderName = "Anonymous"
	}

	wantSSE := strings.Contains(r.Header.Get("Accept"), "text/event-stream")

	// Subscribe BEFORE publishing so our own user bubble is delivered on
	// this connection (rather than racing with the publish).
	var ch <-chan string
	var unsub func()
	if wantSSE {
		ch, unsub = s.hub.subscribe(g.Folder, topic)
		defer unsub()
	}

	id := core.MsgID("msg")
	m := core.Message{
		ID:        id,
		ChatJID:   "web:" + g.Folder,
		Sender:    sender,
		Name:      senderName,
		Content:   content,
		Timestamp: time.Now(),
		Topic:     topic,
	}
	if err := s.st.PutMessage(m); err != nil {
		http.Error(w, "store failed", http.StatusInternalServerError)
		return
	}

	if err := s.rc.SendMessage(chanlib.InboundMsg{
		ID:         m.ID,
		ChatJID:    m.ChatJID,
		Sender:     sender,
		SenderName: senderName,
		Content:    content,
		Timestamp:  m.Timestamp.Unix(),
	}); err != nil {
		http.Error(w, "router unavailable", http.StatusBadGateway)
		return
	}

	payload, _ := json.Marshal(map[string]any{
		"id":         m.ID,
		"role":       "user",
		"content":    m.Content,
		"sender":     senderName,
		"created_at": m.Timestamp.Format(time.RFC3339),
	})
	s.hub.publish(g.Folder, topic, "message", string(payload))

	if wantSSE {
		serveSSE(w, r, ch)
		return
	}

	w.Header().Set("Content-Type", "text/html")
	fmt.Fprintf(w, `<div class="msg user" id="msg-%s">%s</div>`, m.ID, htmlEscape(content))
}

// handleSlinkStream opens an SSE connection for a group/topic.
// GET /slink/stream?group=<folder>&topic=<t>
func (s *server) handleSlinkStream(w http.ResponseWriter, r *http.Request) {
	folder := r.URL.Query().Get("group")
	topic := r.URL.Query().Get("topic")
	if folder == "" || topic == "" {
		http.Error(w, "group and topic required", http.StatusBadRequest)
		return
	}

	// Replay missed messages on reconnect.
	if lastID := r.Header.Get("Last-Event-Id"); lastID != "" {
		if t, ok := s.st.MessageTimestampByID(lastID, "web:"+folder); ok {
			msgs, _ := s.st.MessagesSinceTopic(folder, topic, t, 50)
			flusher, _ := w.(http.Flusher)
			for _, m := range msgs {
				role := "user"
				if m.BotMsg {
					role = "assistant"
				}
				data, _ := json.Marshal(map[string]any{
					"id": m.ID, "role": role, "content": m.Content,
					"created_at": m.Timestamp.Format(time.RFC3339),
				})
				fmt.Fprintf(w, "event: message\ndata: %s\n\n", data)
				if flusher != nil {
					flusher.Flush()
				}
			}
		}
	}

	ch, unsub := s.hub.subscribe(folder, topic)
	defer unsub()
	serveSSE(w, r, ch)
}

func htmlEscape(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, "\"", "&#34;")
	return s
}
