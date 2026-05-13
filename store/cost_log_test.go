package store

import (
	"testing"
	"time"
)

func TestLogCost_RoundTrip(t *testing.T) {
	s, _ := OpenMem()
	defer s.Close()

	if err := s.LogCost(CostRow{
		Folder: "atlas/eng", UserSub: "google:alice",
		Model: "claude-opus-4-7",
		InputTok: 1000, OutputTok: 500, Cents: 23,
	}); err != nil {
		t.Fatalf("LogCost: %v", err)
	}
	got, err := s.SpendTodayFolder("atlas/eng")
	if err != nil {
		t.Fatalf("SpendTodayFolder: %v", err)
	}
	if got != 23 {
		t.Errorf("folder spend = %d, want 23", got)
	}
	got, err = s.SpendTodayUser("google:alice")
	if err != nil {
		t.Fatalf("SpendTodayUser: %v", err)
	}
	if got != 23 {
		t.Errorf("user spend = %d, want 23", got)
	}
}

func TestSpendTodayFolder_Aggregates(t *testing.T) {
	s, _ := OpenMem()
	defer s.Close()

	for _, c := range []int{10, 20, 30} {
		if err := s.LogCost(CostRow{Folder: "f", Model: "m", Cents: c}); err != nil {
			t.Fatalf("LogCost: %v", err)
		}
	}
	got, err := s.SpendTodayFolder("f")
	if err != nil {
		t.Fatalf("SpendTodayFolder: %v", err)
	}
	if got != 60 {
		t.Errorf("sum = %d, want 60", got)
	}
}

func TestSpendTodayFolder_IgnoresYesterday(t *testing.T) {
	s, _ := OpenMem()
	defer s.Close()

	yesterday := time.Now().UTC().Add(-25 * time.Hour)
	if err := s.LogCost(CostRow{
		TS: yesterday, Folder: "f", Model: "m", Cents: 999,
	}); err != nil {
		t.Fatalf("LogCost old: %v", err)
	}
	if err := s.LogCost(CostRow{Folder: "f", Model: "m", Cents: 5}); err != nil {
		t.Fatalf("LogCost new: %v", err)
	}
	got, err := s.SpendTodayFolder("f")
	if err != nil {
		t.Fatalf("SpendTodayFolder: %v", err)
	}
	if got != 5 {
		t.Errorf("today spend = %d, want 5 (yesterday should be excluded)", got)
	}
}

func TestSpendTodayFolder_ScopedByFolder(t *testing.T) {
	s, _ := OpenMem()
	defer s.Close()

	if err := s.LogCost(CostRow{Folder: "a", Model: "m", Cents: 10}); err != nil {
		t.Fatalf("LogCost a: %v", err)
	}
	if err := s.LogCost(CostRow{Folder: "b", Model: "m", Cents: 20}); err != nil {
		t.Fatalf("LogCost b: %v", err)
	}
	gotA, _ := s.SpendTodayFolder("a")
	gotB, _ := s.SpendTodayFolder("b")
	if gotA != 10 || gotB != 20 {
		t.Errorf("a=%d b=%d, want 10 20", gotA, gotB)
	}
}

func TestSpendTodayFolder_EmptyWhenNoRows(t *testing.T) {
	s, _ := OpenMem()
	defer s.Close()

	got, err := s.SpendTodayFolder("never")
	if err != nil {
		t.Fatalf("SpendTodayFolder: %v", err)
	}
	if got != 0 {
		t.Errorf("got %d, want 0", got)
	}
}

func TestSetFolderCap_RoundTrip(t *testing.T) {
	s, _ := OpenMem()
	defer s.Close()

	if _, err := s.db.Exec(
		`INSERT INTO groups (folder, name, added_at) VALUES (?, ?, ?)`,
		"team", "team", "2026-01-01T00:00:00Z"); err != nil {
		t.Fatalf("seed group: %v", err)
	}
	if err := s.SetFolderCap("team", 250); err != nil {
		t.Fatalf("SetFolderCap: %v", err)
	}
	got, err := s.FolderCap("team")
	if err != nil {
		t.Fatalf("FolderCap: %v", err)
	}
	if got != 250 {
		t.Errorf("cap = %d, want 250", got)
	}
}

func TestFolderCap_ZeroWhenUnset(t *testing.T) {
	s, _ := OpenMem()
	defer s.Close()

	if _, err := s.db.Exec(
		`INSERT INTO groups (folder, name, added_at) VALUES (?, ?, ?)`,
		"f", "f", "2026-01-01T00:00:00Z"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	got, err := s.FolderCap("f")
	if err != nil {
		t.Fatalf("FolderCap: %v", err)
	}
	if got != 0 {
		t.Errorf("unset cap = %d, want 0", got)
	}
}

func TestUserCap_RoundTrip(t *testing.T) {
	s, _ := OpenMem()
	defer s.Close()

	if _, err := s.db.Exec(
		`INSERT INTO auth_users (sub, username, hash, name, created_at)
		 VALUES (?, ?, ?, ?, ?)`,
		"google:bob", "bob", "x", "Bob", "2026-01-01T00:00:00Z"); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if err := s.SetUserCap("google:bob", 100); err != nil {
		t.Fatalf("SetUserCap: %v", err)
	}
	got, err := s.UserCap("google:bob")
	if err != nil {
		t.Fatalf("UserCap: %v", err)
	}
	if got != 100 {
		t.Errorf("cap = %d, want 100", got)
	}
}
