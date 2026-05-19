package store

import (
	"testing"
	"time"
)

func TestLogSecretUse_RoundTrip(t *testing.T) {
	s, _ := OpenMem()
	defer s.Close()

	ts := time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC)
	row := SecretUseRow{
		TS:        ts,
		SpawnID:   "spawn-1",
		CallerSub: "github:alice",
		Folder:    "atlas/eng",
		Tool:      "github_pr",
		Key:       "GITHUB_TOKEN",
		Scope:     "user",
		Status:    "ok",
		LatencyMS: 42,
	}
	if err := s.LogSecretUse(row); err != nil {
		t.Fatalf("LogSecretUse: %v", err)
	}

	var (
		spawn, sub, folder, tool, key, scope, status string
		latency                                      int64
	)
	if err := s.db.QueryRow(
		`SELECT spawn_id, caller_sub, folder, tool, key, scope, status, latency_ms
		 FROM secret_use_log`,
	).Scan(&spawn, &sub, &folder, &tool, &key, &scope, &status, &latency); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if spawn != "spawn-1" || sub != "github:alice" || folder != "atlas/eng" ||
		tool != "github_pr" || key != "GITHUB_TOKEN" || scope != "user" ||
		status != "ok" || latency != 42 {
		t.Errorf("row mismatch: spawn=%q sub=%q folder=%q tool=%q key=%q scope=%q status=%q latency=%d",
			spawn, sub, folder, tool, key, scope, status, latency)
	}
}

func TestLogSecretUse_AutoTimestamp(t *testing.T) {
	s, _ := OpenMem()
	defer s.Close()

	if err := s.LogSecretUse(SecretUseRow{Tool: "t", Key: "K", Scope: "missing", Status: "ok"}); err != nil {
		t.Fatalf("LogSecretUse: %v", err)
	}
	var ts string
	if err := s.db.QueryRow(`SELECT ts FROM secret_use_log`).Scan(&ts); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if _, err := time.Parse(time.RFC3339Nano, ts); err != nil {
		t.Errorf("ts not RFC3339Nano: %q (%v)", ts, err)
	}
}

func TestLookupSecret_HitAndMiss(t *testing.T) {
	s, _ := OpenMem()
	defer s.Close()

	if err := s.SetSecret(ScopeUser, "github:alice", "GITHUB_TOKEN", "ghp_xxx"); err != nil {
		t.Fatal(err)
	}
	v, ok := s.LookupSecret(ScopeUser, "github:alice", "GITHUB_TOKEN")
	if !ok || v != "ghp_xxx" {
		t.Errorf("hit: v=%q ok=%v want ghp_xxx,true", v, ok)
	}
	_, ok = s.LookupSecret(ScopeUser, "github:alice", "NOPE")
	if ok {
		t.Error("miss: ok=true, want false")
	}
}
