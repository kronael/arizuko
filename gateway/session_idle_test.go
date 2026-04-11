package gateway

import (
	"testing"
	"time"

	"github.com/onvos/arizuko/core"
	"github.com/onvos/arizuko/store"
)

func TestSessionIdleExpired(t *testing.T) {
	s, err := store.OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	cfg := &core.Config{}
	gw := &Gateway{cfg: cfg, store: s}

	const jid = "telegram:42"

	// No cursor yet: never expired (fresh chat, no session anyway).
	if gw.sessionIdleExpired(jid) {
		t.Error("expected false for zero cursor")
	}

	// Cursor 1h ago: fresh.
	s.SetAgentCursor(jid, time.Now().Add(-1*time.Hour))
	if gw.sessionIdleExpired(jid) {
		t.Error("expected false for 1h-old cursor")
	}

	// Cursor 3d ago: expired.
	s.SetAgentCursor(jid, time.Now().Add(-3*24*time.Hour))
	if !gw.sessionIdleExpired(jid) {
		t.Error("expected true for 3d-old cursor")
	}

	// Exactly at threshold: not expired (strict >).
	s.SetAgentCursor(jid, time.Now().Add(-sessionIdleExpiry).Add(time.Second))
	if gw.sessionIdleExpired(jid) {
		t.Error("expected false for just-under-threshold cursor")
	}
}

// TestSpawnResetsStaleSession verifies the spawn-site decision:
// a chat with a 3-day-old cursor and a stored session id gets the
// session cleared before the container runs, so the next spawn starts
// fresh instead of resuming.
func TestSpawnResetsStaleSession(t *testing.T) {
	s, err := store.OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	const folder = "main"
	const topic = ""
	const jid = "telegram:42"
	const staleID = "sid-week-old"

	if err := s.PutGroup(core.Group{
		Name: folder, Folder: folder,
		AddedAt: time.Now(), State: "active",
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSession(folder, topic, staleID); err != nil {
		t.Fatal(err)
	}
	s.SetAgentCursor(jid, time.Now().Add(-3*24*time.Hour))

	gw := &Gateway{cfg: &core.Config{}, store: s}
	if !gw.sessionIdleExpired(jid) {
		t.Fatal("precondition: expected cursor to be stale")
	}

	// Inline the spawn-site decision block (runAgentWithOpts lines 634-641).
	sessionID, _ := s.GetSession(folder, topic)
	if sessionID != "" && gw.sessionIdleExpired(jid) {
		s.DeleteSession(folder, topic)
		sessionID = ""
	}

	if sessionID != "" {
		t.Errorf("stale session id not cleared: %q", sessionID)
	}
	if got, ok := s.GetSession(folder, topic); ok && got != "" {
		t.Errorf("session row not deleted: got %q", got)
	}
}

// TestSpawnKeepsFreshSession: active chat keeps its session id.
func TestSpawnKeepsFreshSession(t *testing.T) {
	s, err := store.OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	const folder = "main"
	const topic = ""
	const jid = "telegram:42"
	const freshID = "sid-active"

	s.PutGroup(core.Group{Name: folder, Folder: folder, AddedAt: time.Now(), State: "active"})
	s.SetSession(folder, topic, freshID)
	s.SetAgentCursor(jid, time.Now().Add(-2*time.Hour))

	gw := &Gateway{cfg: &core.Config{}, store: s}

	sessionID, _ := s.GetSession(folder, topic)
	if sessionID != "" && gw.sessionIdleExpired(jid) {
		s.DeleteSession(folder, topic)
		sessionID = ""
	}

	if sessionID != freshID {
		t.Errorf("fresh session id cleared: got %q want %q", sessionID, freshID)
	}
}
