package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/onvos/arizuko/chanlib"
	"github.com/onvos/arizuko/core"
)

// frame is the JSON shape returned for each assistant output of a round.
// Mirrors the SSE "message" event payload from handleSend so callers can
// reuse parsing across snapshot, page, and SSE.
type frame struct {
	ID        string `json:"id"`
	Content   string `json:"content"`
	CreatedAt string `json:"created_at"`
	Kind      string `json:"kind"` // "message" or "status"
	TurnID    string `json:"turn_id"`
}

func messageFrames(msgs []core.Message) []frame {
	out := make([]frame, 0, len(msgs))
	for _, m := range msgs {
		kind := "message"
		if strings.HasPrefix(m.Content, "⏳ ") {
			kind = "status"
		}
		out = append(out, frame{
			ID:        m.ID,
			Content:   m.Content,
			CreatedAt: m.Timestamp.Format(time.RFC3339),
			Kind:      kind,
			TurnID:    m.TurnID,
		})
	}
	return out
}

// authorizeTurn verifies the slink token resolves to a group and the
// turn_id belongs to that group's web chat. Returns the resolved folder
// or writes a 4xx and returns "".
func (s *server) authorizeTurn(w http.ResponseWriter, r *http.Request) (folder, turnID string) {
	token := r.PathValue("token")
	turnID = r.PathValue("id")
	g, ok := s.st.GroupBySlinkToken(token)
	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return "", ""
	}
	jid := "web:" + g.Folder
	// Defensive cross-check: turn_id must be a real message in this folder's chat.
	if _, ok := s.st.MessageTimestampByID(turnID, jid); !ok {
		http.Error(w, "turn not found", http.StatusNotFound)
		return "", ""
	}
	return g.Folder, turnID
}

// handleTurnSnapshot: GET /slink/<token>/turn/<id>[?after=<msg_id>]
// Returns status + assistant frames produced during the round.
func (s *server) handleTurnSnapshot(w http.ResponseWriter, r *http.Request) {
	folder, turnID := s.authorizeTurn(w, r)
	if folder == "" {
		return
	}
	after := r.URL.Query().Get("after")
	msgs, err := s.st.TurnFrames(turnID, after, 200)
	if err != nil {
		slog.Error("turn frames query", "folder", folder, "turn", turnID, "err", err)
		chanlib.WriteErr(w, http.StatusInternalServerError, "query failed")
		return
	}
	info, err := s.st.GetTurnResult(folder, turnID)
	if err != nil {
		slog.Error("turn result query", "folder", folder, "turn", turnID, "err", err)
		chanlib.WriteErr(w, http.StatusInternalServerError, "query failed")
		return
	}
	frames := messageFrames(msgs)
	last := ""
	if len(frames) > 0 {
		last = frames[len(frames)-1].ID
	}
	chanlib.WriteJSON(w, map[string]any{
		"turn_id":        turnID,
		"status":         info.Status,
		"frames":         frames,
		"last_frame_id":  last,
	})
}

// handleTurnStatus: GET /slink/<token>/turn/<id>/status
// Cheap status check — no frame payloads, just status + counts. Lets
// pollers decide between "stop", "poll once more", or "open SSE".
func (s *server) handleTurnStatus(w http.ResponseWriter, r *http.Request) {
	folder, turnID := s.authorizeTurn(w, r)
	if folder == "" {
		return
	}
	info, err := s.st.GetTurnResult(folder, turnID)
	if err != nil {
		chanlib.WriteErr(w, http.StatusInternalServerError, "query failed")
		return
	}
	msgs, err := s.st.TurnFrames(turnID, "", 200)
	if err != nil {
		chanlib.WriteErr(w, http.StatusInternalServerError, "query failed")
		return
	}
	last := ""
	if n := len(msgs); n > 0 {
		last = msgs[n-1].ID
	}
	chanlib.WriteJSON(w, map[string]any{
		"turn_id":       turnID,
		"status":        info.Status,
		"frames_count":  len(msgs),
		"last_frame_id": last,
	})
}

// handleTurnSSE: GET /slink/<token>/turn/<id>/sse
// Subscribes to the chat's hub channel, filters frames by turn_id, emits
// any backlog (Last-Event-Id or initial catch-up) and live events until
// round_done arrives. Then closes.
func (s *server) handleTurnSSE(w http.ResponseWriter, r *http.Request) {
	folder, turnID := s.authorizeTurn(w, r)
	if folder == "" {
		return
	}
	jid := "web:" + folder
	topic := s.st.TopicByMessageID(turnID, jid)
	if topic == "" {
		http.Error(w, "turn topic missing", http.StatusInternalServerError)
		return
	}

	if !s.hub.canSubscribe() {
		http.Error(w, "too many subscriptions", http.StatusServiceUnavailable)
		return
	}

	// Subscribe BEFORE flushing backlog so we don't miss events that land
	// between the catch-up query and the live attach.
	ch, unsub := s.hub.subscribe(folder, topic)
	defer unsub()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	flusher, _ := w.(http.Flusher)

	// Backlog catch-up: replay frames from afterID (Last-Event-Id) onward.
	after := r.Header.Get("Last-Event-Id")
	msgs, err := s.st.TurnFrames(turnID, after, 200)
	if err == nil {
		for _, m := range msgs {
			fr := messageFrames([]core.Message{m})[0]
			data, _ := json.Marshal(fr)
			writeSSE(w, flusher, "id: "+fr.ID+"\nevent: "+fr.Kind+"\ndata: "+string(data)+"\n\n")
		}
	}

	// If the round already finished before we subscribed, emit round_done
	// straight away and exit.
	if info, _ := s.st.GetTurnResult(folder, turnID); info.Status != "pending" {
		payload, _ := json.Marshal(map[string]any{
			"turn_id": turnID,
			"status":  info.Status,
		})
		writeSSE(w, flusher, "event: round_done\ndata: "+string(payload)+"\n\n")
		return
	}

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	tick := time.NewTicker(15 * time.Second)
	defer tick.Stop()

	for {
		select {
		case msg, ok := <-ch:
			if !ok {
				return
			}
			ev, payload := splitSSEFrame(msg)
			// Filter "message" events to only those for our turn_id.
			if ev == "message" {
				var p struct {
					TurnID string `json:"turn_id"`
				}
				if json.Unmarshal([]byte(payload), &p) == nil && p.TurnID != turnID {
					continue
				}
			}
			if writeSSE(w, flusher, msg) != nil {
				return
			}
			if ev == "round_done" {
				return
			}
		case <-tick.C:
			if writeSSE(w, flusher, ": ping\n\n") != nil {
				return
			}
		case <-ctx.Done():
			return
		}
	}
}

func writeSSE(w http.ResponseWriter, f http.Flusher, s string) error {
	if _, err := w.Write([]byte(s)); err != nil {
		return err
	}
	if f != nil {
		f.Flush()
	}
	return nil
}

// splitSSEFrame extracts the event name and JSON data from a serialized
// SSE frame as built by hub.publish ("event: <name>\ndata: <json>\n\n").
// Returns ("", "") on malformed input.
func splitSSEFrame(frame string) (event, data string) {
	for _, line := range strings.Split(frame, "\n") {
		switch {
		case strings.HasPrefix(line, "event: "):
			event = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			data = strings.TrimPrefix(line, "data: ")
		}
	}
	return event, data
}
