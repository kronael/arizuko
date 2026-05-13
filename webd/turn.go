package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/kronael/arizuko/chanlib"
	"github.com/kronael/arizuko/core"
)

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

func (s *server) authorizeTurn(w http.ResponseWriter, r *http.Request) (folder, turnID string) {
	token := r.PathValue("token")
	turnID = r.PathValue("id")
	g, ok := s.st.GroupBySlinkToken(token)
	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return "", ""
	}
	jid := "web:" + g.Folder
	if _, ok := s.st.MessageTimestampByID(turnID, jid); !ok {
		http.Error(w, "turn not found", http.StatusNotFound)
		return "", ""
	}
	return g.Folder, turnID
}

// GET /slink/<token>/turn/<id>[?after=<msg_id>]
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

// GET /slink/<token>/turn/<id>/status — status + counts only, no frame payloads.
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

// GET /slink/<token>/turn/<id>/sse — streams frames for one turn, closes on round_done.
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

	ch, unsub := s.hub.subscribe(folder, topic)
	defer unsub()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	flusher, _ := w.(http.Flusher)

	after := r.Header.Get("Last-Event-Id")
	msgs, err := s.st.TurnFrames(turnID, after, 200)
	if err == nil {
		for _, m := range msgs {
			fr := messageFrames([]core.Message{m})[0]
			data, _ := json.Marshal(fr)
			writeSSE(w, flusher, "id: "+fr.ID+"\nevent: "+fr.Kind+"\ndata: "+string(data)+"\n\n")
		}
	}

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
