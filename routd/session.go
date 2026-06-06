package routd

import (
	"context"
	"time"

	"github.com/kronael/arizuko/core"
	runedv1 "github.com/kronael/arizuko/runed/api/v1"
)

// SessionResolver federates the new_session continuity hint + inspect_session
// run history: runed OWNS session_log (it writes a row per spawn), so routd
// fetches the n newest rows over GET /v1/sessions/recent instead of opening
// runed.db cross-DB. nil resolver / unreachable runed → nil (no prior session),
// the same shape routd rendered against an absent runed.db sibling.
type SessionResolver interface {
	RecentSessions(folder string, n int) []core.SessionRecord
}

// httpSessions backs SessionResolver with routd's existing runed client (the
// one already used for POST /v1/runs) — same service:routd bearer, same base
// URL. No new auth wiring.
type httpSessions struct {
	c *runedv1.Client
}

// NewSessionResolver wraps the runed client. nil client → nil resolver (the
// caller then renders "no prior session", like routd against an absent runed.db).
func NewSessionResolver(c *runedv1.Client) SessionResolver {
	if c == nil {
		return nil
	}
	return &httpSessions{c: c}
}

// RecentSessions issues GET /v1/sessions/recent and maps the wire rows to
// core.SessionRecord. Any transport/auth error or non-200 returns nil —
// advisory only, never an error path (faithful to the old nil-sibling guard).
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
