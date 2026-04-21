package main

import (
	"testing"
	"time"

	"github.com/onvos/arizuko/core"
	"github.com/onvos/arizuko/store"
)

func seedRootAndChat(t *testing.T, s *store.Store) {
	t.Helper()
	if err := s.PutGroup(core.Group{
		Folder:  "main",
		Name:    "main",
		Parent:  "",
		AddedAt: time.Now(),
		State:   "active",
	}); err != nil {
		t.Fatal(err)
	}
	// Known jid — seed via cursor write.
	s.SetAgentCursor("tg:42", time.Now().Add(-time.Hour))
}

func TestAnnounceStartup_InsertsOnce(t *testing.T) {
	s, err := store.OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	seedRootAndChat(t, s)

	if err := s.InsertAnnouncement(store.Announcement{
		Service: "store", Version: 99, Body: "upgrade!",
	}); err != nil {
		t.Fatal(err)
	}

	if err := announceStartup(s); err != nil {
		t.Fatal(err)
	}
	flushed := s.FlushSysMsgs("main")
	if flushed == "" {
		t.Fatal("expected system message after first announceStartup")
	}

	// Second call: no new sysmsg. Re-announce only inserts if absent.
	// Since FlushSysMsgs deleted the previous one, we also have to
	// re-insert the pending announcement state (still pending).
	if err := announceStartup(s); err != nil {
		t.Fatal(err)
	}
	flushed2 := s.FlushSysMsgs("main")
	// Still pending → will insert again after flush.
	if flushed2 == "" {
		t.Fatal("expected second insert after prior flush (still pending)")
	}

	// Now simulate two consecutive startups without flushing in between:
	// the second must NOT duplicate.
	if err := announceStartup(s); err != nil {
		t.Fatal(err)
	}
	if err := announceStartup(s); err != nil {
		t.Fatal(err)
	}
	var n int
	s.DB().QueryRow(
		`SELECT COUNT(*) FROM system_messages
		 WHERE group_id='main' AND origin='migration'`,
	).Scan(&n)
	if n != 1 {
		t.Fatalf("duplicate sys messages: got %d, want 1", n)
	}
}

func TestAnnounceStartup_NoPending_NoMsg(t *testing.T) {
	s, err := store.OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	seedRootAndChat(t, s)

	if err := announceStartup(s); err != nil {
		t.Fatal(err)
	}
	if got := s.FlushSysMsgs("main"); got != "" {
		t.Fatalf("expected no sysmsg, got %q", got)
	}
}
