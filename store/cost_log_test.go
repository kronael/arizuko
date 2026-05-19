package store

import (
	"fmt"
	"testing"
	"time"
)

func TestGroupUsageBulk_Empty(t *testing.T) {
	s, _ := OpenMem()
	defer s.Close()
	out, err := s.GroupUsageBulk(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 0 {
		t.Errorf("got %d, want 0", len(out))
	}
}

func TestGroupUsageBulk_TokensAndMessages(t *testing.T) {
	s, _ := OpenMem()
	defer s.Close()

	// Seed cost_log rows for two folders.
	for _, r := range []CostRow{
		{Folder: "a", Model: "m", InputTok: 100, OutputTok: 50, Cents: 10},
		{Folder: "a", Model: "m", InputTok: 200, OutputTok: 100, Cents: 20},
		{Folder: "b", Model: "m", InputTok: 300, OutputTok: 150, Cents: 30},
	} {
		if err := s.LogCost(r); err != nil {
			t.Fatalf("LogCost: %v", err)
		}
	}

	// Seed messages with routed_to.
	ts := time.Now().UTC().Format(time.RFC3339Nano)
	for i, folder := range []string{"a", "a", "b"} {
		_, err := s.db.Exec(
			`INSERT INTO messages
			 (id, chat_jid, sender, content, timestamp, routed_to)
			 VALUES (?, ?, '', '', ?, ?)`,
			fmt.Sprintf("id%d", i), "jid", ts, folder)
		if err != nil {
			t.Fatalf("seed message %d: %v", i, err)
		}
	}

	out, err := s.GroupUsageBulk([]string{"a", "b"})
	if err != nil {
		t.Fatalf("GroupUsageBulk: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("len = %d, want 2", len(out))
	}
	byFolder := map[string]GroupUsageSummary{}
	for _, u := range out {
		byFolder[u.Folder] = u
	}
	if got := byFolder["a"].Tokens7d; got != 450 { // 100+50+200+100
		t.Errorf("a tokens = %d, want 450", got)
	}
	if got := byFolder["a"].Cents7d; got != 30 {
		t.Errorf("a cents = %d, want 30", got)
	}
	if got := byFolder["a"].MsgCount; got != 2 {
		t.Errorf("a msgs = %d, want 2", got)
	}
	if got := byFolder["b"].Tokens7d; got != 450 { // 300+150
		t.Errorf("b tokens = %d, want 450", got)
	}
	if byFolder["a"].LastActive == "" {
		t.Errorf("a LastActive should be non-empty")
	}
}

func TestGroupUsageBulk_OldCostExcluded(t *testing.T) {
	s, _ := OpenMem()
	defer s.Close()

	old := time.Now().UTC().Add(-8 * 24 * time.Hour)
	if err := s.LogCost(CostRow{
		TS: old, Folder: "x", Model: "m", InputTok: 9999, Cents: 999,
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.LogCost(CostRow{Folder: "x", Model: "m", InputTok: 10, Cents: 5}); err != nil {
		t.Fatal(err)
	}
	out, err := s.GroupUsageBulk([]string{"x"})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].Tokens7d != 10 {
		t.Errorf("tokens = %d, want 10 (old row excluded)", out[0].Tokens7d)
	}
}

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
		`INSERT INTO groups (folder, added_at) VALUES (?, ?)`,
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
		`INSERT INTO groups (folder, added_at) VALUES (?, ?)`,
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
