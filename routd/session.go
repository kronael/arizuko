package routd

import (
	"context"
	"time"

	"github.com/kronael/arizuko/core"
	runedv1 "github.com/kronael/arizuko/runed/api/v1"
)

// SessionResolver federates the new_session continuity hint + inspect_session
// run history: runed OWNS session_log (it writes a row per spawn), so routd
// fetches the n newest rows over GET /v1/spawn_sessions/recent. nil resolver /
// unreachable runed → nil (no prior session).
type SessionResolver interface {
	RecentSessions(folder string, n int) []core.SessionRecord
}

// httpSessions backs SessionResolver with routd's runed client.
type httpSessions struct {
	c *runedv1.Client
}

// NewSessionResolver wraps the runed client.
func NewSessionResolver(c *runedv1.Client) SessionResolver {
	return &httpSessions{c: c}
}

// RecentSessions issues GET /v1/spawn_sessions/recent. Any error returns nil
// — advisory only, never an error path.
func (h *httpSessions) RecentSessions(folder string, n int) []core.SessionRecord {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	resp, err := h.c.RecentSessions(ctx, folder, n)
	if err != nil {
		return nil
	}
	out := make([]core.SessionRecord, 0, len(resp.Sessions))
	for _, s := range resp.Sessions {
		sr := core.SessionRecord{
			ID: s.ID, Folder: s.GroupFolder, SessionID: s.SessionID,
			Result: s.Result, Error: s.Error, MsgCount: s.MessageCount,
		}
		sr.StartedAt, _ = time.Parse(time.RFC3339, s.StartedAt)
		if s.EndedAt != "" {
			t, _ := time.Parse(time.RFC3339, s.EndedAt)
			sr.EndedAt = &t
		}
		out = append(out, sr)
	}
	return out
}
