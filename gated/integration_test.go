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
	s.SetAgentCursor("tg:42", time.Now().Add(-time.Hour))
}

func TestAnnounceStartup_InsertsOnce(t *testing.T) {
	s, err := store.OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	seedRootAndChat(t, s)

	if err := announceStartup(s); err != nil {
		t.Fatal(err)
	}
	if s.FlushSysMsgs("main") == "" {
		t.Fatal("expected system message after first announceStartup")
	}

	// Second call after flush: still pending (no RecordSent), re-inserts.
	if err := announceStartup(s); err != nil {
		t.Fatal(err)
	}
	if s.FlushSysMsgs("main") == "" {
		t.Fatal("expected second insert after prior flush (still pending)")
	}

	// Two consecutive startups without flushing → must not duplicate.
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

func TestAnnounceStartup_AllDelivered_NoMsg(t *testing.T) {
	s, err := store.OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	seedRootAndChat(t, s)

	for _, a := range s.Announcements() {
		if err := s.RecordSent(a.Service, a.Version, "tg:42"); err != nil {
			t.Fatal(err)
		}
	}

	if err := announceStartup(s); err != nil {
		t.Fatal(err)
	}
	if got := s.FlushSysMsgs("main"); got != "" {
		t.Fatalf("expected no sysmsg, got %q", got)
	}
}
