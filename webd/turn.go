package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sort"
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

// verbStatus is the verb of a turn's progress row (routd/turn_status.go). The
// row's verb, not its text, says it is progress: the text is the agent's.
const verbStatus = "status"

// frameKind is the one place a row becomes a frame kind. Replay
// (messageFrames) and the live replace frame (handleEdit) both call it.
func frameKind(m core.Message) string {
	if m.Verb == verbStatus {
		return "status"
	}
	return "message"
}

func messageFrames(msgs []core.Message) []frame {
	out := make([]frame, 0, len(msgs))
	for _, m := range msgs {
		id := m.ID
		if m.Verb == verbStatus && m.PlatformID != "" {
			id = m.PlatformID
		}
		out = append(out, frame{
			ID:        id,
			Content:   m.Content,
			CreatedAt: m.Timestamp.Format(time.RFC3339),
			Kind:      frameKind(m),
			TurnID:    m.TurnID,
		})
	}
	return out
}

func mergeProgress(msgs, active []core.Message) []core.Message {
	all := append(append([]core.Message{}, msgs...), active...)
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].Timestamp.Equal(all[j].Timestamp) {
			return all[i].ID < all[j].ID
		}
		return all[i].Timestamp.Before(all[j].Timestamp)
	})
	return all
}

func bufferedProgress(ch <-chan string, active []frame) ([]string, []frame) {
	var queued []string
	for {
		select {
		case msg, ok := <-ch:
			if !ok {
				return queued, active
			}
			queued = append(queued, msg)
			event, data := splitSSEFrame(msg)
			if event != "delete" {
				continue
			}
			var p struct {
				ID     string `json:"id"`
				TurnID string `json:"turn_id"`
			}
			if json.Unmarshal([]byte(data), &p) != nil {
				continue
			}
			kept := active[:0]
			for _, f := range active {
				if f.ID != p.ID || f.TurnID != p.TurnID {
					kept = append(kept, f)
				}
			}
			active = kept
		default:
			return queued, active
		}
	}
}

func progressEvent(frames []frame) string {
	data, _ := json.Marshal(map[string]any{"frames": frames})
	return "event: progress\ndata: " + string(data) + "\n\n"
}

func (s *server) currentProgress(chatJID, topic, turnID string, frames []frame) ([]frame, error) {
	rows, err := s.stRoutd.ActiveProgress(chatJID, topic, turnID)
	if err != nil {
		return nil, err
	}
	current := make(map[string]frame)
	for _, f := range messageFrames(rows) {
		current[f.ID] = f
	}
	kept := frames[:0]
	for _, f := range frames {
		if latest, ok := current[f.ID]; ok {
			kept = append(kept, latest)
		}
	}
	return kept, nil
}

func (s *server) activeProgressEvent(chatJID, topic, msg string) bool {
	event, payload := splitSSEFrame(msg)
	if event != "message" && event != "status" {
		return true
	}
	var p struct {
		ID     string `json:"id"`
		TurnID string `json:"turn_id"`
		Kind   string `json:"kind"`
	}
	if json.Unmarshal([]byte(payload), &p) != nil {
		return false
	}
	if p.Kind != "status" && event != "status" {
		return true
	}
	rows, err := s.stRoutd.ActiveProgress(chatJID, topic, p.TurnID)
	if err != nil {
		slog.Error("live progress query", "err", err)
		return false
	}
	for _, f := range messageFrames(rows) {
		if f.ID == p.ID && f.TurnID == p.TurnID {
			return true
		}
	}
	return false
}

// route_tokens + messages live in routd.db (stRoutd) — split ownership
func (s *server) authorizeTurn(w http.ResponseWriter, r *http.Request) (folder, chatJID, turnID string) {
	token := r.PathValue("token")
	turnID = r.PathValue("id")
	row, ok := s.stRoutd.LookupRouteToken(token)
	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return "", "", ""
	}
	folder = row.Folder()
	if _, ok := s.stRoutd.MessageTimestampByID(turnID, row.JID); !ok {
		http.Error(w, "turn not found", http.StatusNotFound)
		return "", "", ""
	}
	return folder, row.JID, turnID
}

// GET /slink/<token>/turn/<id>[?after=<msg_id>]
// turn_results + messages live in routd.db (stRoutd) — split ownership
func (s *server) handleTurnSnapshot(w http.ResponseWriter, r *http.Request) {
	folder, chatJID, turnID := s.authorizeTurn(w, r)
	if folder == "" {
		return
	}
	after := r.URL.Query().Get("after")
	msgs, err := s.stRoutd.TurnFrames(chatJID, turnID, after, 200)
	if err != nil {
		slog.Error("turn frames query", "folder", folder, "turn", turnID, "err", err)
		chanlib.WriteErr(w, http.StatusInternalServerError, "query failed")
		return
	}
	info, err := s.stRoutd.GetTurnResult(folder, turnID)
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
	active, err := s.stRoutd.ActiveProgress(chatJID, "", turnID)
	if err != nil {
		chanlib.WriteErr(w, http.StatusInternalServerError, "query failed")
		return
	}
	frames = messageFrames(mergeProgress(msgs, active))
	chanlib.WriteJSON(w, map[string]any{
		"turn_id":       turnID,
		"status":        info.Status,
		"frames":        frames,
		"last_frame_id": last,
	})
}

// GET /slink/<token>/turn/<id>/status — status + counts only, no frame payloads.
func (s *server) handleTurnStatus(w http.ResponseWriter, r *http.Request) {
	folder, chatJID, turnID := s.authorizeTurn(w, r)
	if folder == "" {
		return
	}
	info, err := s.stRoutd.GetTurnResult(folder, turnID)
	if err != nil {
		chanlib.WriteErr(w, http.StatusInternalServerError, "query failed")
		return
	}
	msgs, err := s.stRoutd.TurnFrames(chatJID, turnID, "", 200)
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
	folder, chatJID, turnID := s.authorizeTurn(w, r)
	if folder == "" {
		return
	}
	// The hub is keyed by the chat JID verbatim (routd publishes round_done on
	// tc.ChatJID), NOT the token's target folder — the two differ whenever the
	// token was minted with a jid suffix.
	topic := s.stRoutd.TopicByMessageID(turnID, chatJID)
	if topic == "" {
		http.Error(w, "turn topic missing", http.StatusInternalServerError)
		return
	}

	if !s.hub.canSubscribe() {
		http.Error(w, "too many subscriptions", http.StatusServiceUnavailable)
		return
	}

	ch, unsub := s.hub.subscribe(chatJID, topic)
	defer unsub()

	sseHeaders(w)
	flusher, _ := w.(http.Flusher)
	// A turn with nothing to replay would otherwise hold the headers back until
	// its first live frame.
	if flusher != nil {
		flusher.Flush()
	}

	after := r.Header.Get("Last-Event-Id")
	msgs, err := s.stRoutd.TurnFrames(chatJID, turnID, after, 200)
	if err != nil {
		slog.Error("turn SSE: replay query failed; the client resumes from a gap it cannot see",
			"chat_jid", chatJID, "turn_id", turnID, "after", after, "err", err)
	}
	if err == nil {
		for _, m := range msgs {
			fr := messageFrames([]core.Message{m})[0]
			data, _ := json.Marshal(fr)
			if writeSSE(w, flusher, "id: "+fr.ID+"\nevent: "+fr.Kind+"\ndata: "+string(data)+"\n\n") != nil {
				return
			}
		}
	}
	active, err := s.stRoutd.ActiveProgress(chatJID, topic, turnID)
	if err != nil {
		slog.Error("turn SSE: active progress query", "err", err)
		return
	}
	queued, activeFrames := bufferedProgress(ch, messageFrames(active))
	activeFrames, err = s.currentProgress(chatJID, topic, turnID, activeFrames)
	if err != nil {
		slog.Error("turn SSE: recheck progress", "err", err)
		return
	}
	for _, fr := range activeFrames {
		data, _ := json.Marshal(fr)
		if writeSSE(w, flusher, "event: status\ndata: "+string(data)+"\n\n") != nil {
			return
		}
	}
	if writeSSE(w, flusher, progressEvent(activeFrames)) != nil {
		return
	}

	// A read failure must not read as a finished round. GetTurnResult maps only
	// sql.ErrNoRows to "pending"; a real error returns the zero TurnInfo, whose
	// Status is "" — and "" != "pending", so the discarded error used to emit
	// round_done with an empty status and close the stream, telling the browser
	// the turn was over (BUGS L83). The sibling JSON handlers 500 on the same
	// query; headers are already sent here, so the truth rides the status field
	// the client already reads.
	info, terr := s.stRoutd.GetTurnResult(folder, turnID)
	if terr != nil {
		slog.Error("turn SSE: turn-result read failed; ending the round as error, not as done",
			"folder", folder, "turn_id", turnID, "err", terr)
		info.Status = "error"
	}
	if terr != nil || info.Status != "pending" {
		payload, _ := json.Marshal(map[string]any{
			"turn_id": turnID,
			"status":  info.Status,
		})
		_ = writeSSE(w, flusher, "event: round_done\ndata: "+string(payload)+"\n\n")
		return
	}

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	tick := time.NewTicker(15 * time.Second)
	defer tick.Stop()

	for {
		if len(queued) > 0 {
			msg := queued[0]
			queued = queued[1:]
			if !s.writeTurnEvent(w, flusher, chatJID, turnID, msg) {
				return
			}
			continue
		}
		select {
		case msg, ok := <-ch:
			if !ok {
				return
			}
			if !s.writeTurnEvent(w, flusher, chatJID, turnID, msg) {
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

func (s *server) writeTurnEvent(w http.ResponseWriter, flusher http.Flusher, chatJID, turnID, msg string) bool {
	event, payload := splitSSEFrame(msg)
	if event == "message" || event == "status" || event == "delete" || event == "round_done" {
		var p struct {
			ID     string `json:"id"`
			TurnID string `json:"turn_id"`
			Kind   string `json:"kind"`
		}
		if json.Unmarshal([]byte(payload), &p) != nil || p.TurnID != turnID {
			return true
		}
		if p.Kind == "status" || event == "status" {
			if !s.activeProgressEvent(chatJID, "", msg) {
				return true
			}
			msg = "event: status\ndata: " + payload + "\n\n"
		}
	}
	return writeSSE(w, flusher, msg) == nil && event != "round_done"
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
	for line := range strings.SplitSeq(frame, "\n") {
		switch {
		case strings.HasPrefix(line, "event: "):
			event = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			data = strings.TrimPrefix(line, "data: ")
		}
	}
	return event, data
}
