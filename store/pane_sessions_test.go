package store

import (
	"testing"
)

func TestPaneSessions_UpsertGetByChannel(t *testing.T) {
	s, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if err := s.UpsertPane("T1", "U1", "1700000000.000100", "D1"); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	p, ok := s.GetPaneByChannel("D1")
	if !ok {
		t.Fatalf("get by channel: missing")
	}
	if p.TeamID != "T1" || p.UserID != "U1" || p.ThreadTS != "1700000000.000100" || p.ChannelID != "D1" {
		t.Fatalf("get by channel: bad row %+v", p)
	}
	if p.OpenedAt == "" {
		t.Fatalf("get by channel: opened_at empty")
	}
	if p.ContextJID != "" || p.LastStatusAt != "" {
		t.Fatalf("get by channel: unexpected nulls populated %+v", p)
	}
}

func TestPaneSessions_UpsertIdempotent(t *testing.T) {
	s, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if err := s.UpsertPane("T1", "U1", "1700.001", "D1"); err != nil {
		t.Fatal(err)
	}
	first, _ := s.GetPaneByChannel("D1")
	if err := s.UpsertPane("T1", "U1", "1700.001", "D2"); err != nil {
		t.Fatal(err)
	}
	// channel_id refreshes; lookup by D2 now succeeds, D1 doesn't.
	if _, ok := s.GetPaneByChannel("D1"); ok {
		t.Fatalf("D1 should no longer map")
	}
	got, ok := s.GetPaneByChannel("D2")
	if !ok {
		t.Fatalf("D2 missing after channel refresh")
	}
	if got.OpenedAt != first.OpenedAt {
		t.Fatalf("opened_at must not change on upsert")
	}
}

func TestPaneSessions_SetContext(t *testing.T) {
	s, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.UpsertPane("T1", "U1", "1700.001", "D1"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPaneContext("T1", "U1", "1700.001", "slack:T1/channel/C99"); err != nil {
		t.Fatal(err)
	}
	p, _ := s.GetPaneByChannel("D1")
	if p.ContextJID != "slack:T1/channel/C99" {
		t.Fatalf("context not set: %q", p.ContextJID)
	}
	if err := s.SetPaneContext("T1", "U1", "1700.001", ""); err != nil {
		t.Fatal(err)
	}
	p, _ = s.GetPaneByChannel("D1")
	if p.ContextJID != "" {
		t.Fatalf("context not cleared: %q", p.ContextJID)
	}
}

func TestPaneSessions_SetStatusAt(t *testing.T) {
	s, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.UpsertPane("T1", "U1", "1700.001", "D1"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPaneStatusAt("T1", "U1", "1700.001", "2026-05-16T10:00:00Z"); err != nil {
		t.Fatal(err)
	}
	p, _ := s.GetPaneByChannel("D1")
	if p.LastStatusAt != "2026-05-16T10:00:00Z" {
		t.Fatalf("status_at not set: %q", p.LastStatusAt)
	}
}

func TestPaneSessions_GetByChannelMissing(t *testing.T) {
	s, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, ok := s.GetPaneByChannel("Dnope"); ok {
		t.Fatalf("expected missing")
	}
}
