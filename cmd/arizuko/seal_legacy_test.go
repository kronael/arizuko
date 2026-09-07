package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/store"
	"github.com/kronael/arizuko/tests/testutils"
)

// seedLegacyPlaintext writes an unencrypted row the way a build before BUGS S2
// wrote one. No write path can still produce it, so this inserts directly.
func seedLegacyPlaintext(t *testing.T, s *store.Store, scopeID, key, value string) {
	t.Helper()
	if _, err := s.DB().Exec(
		`INSERT INTO secrets (scope_kind, scope_id, key, value, created_at) VALUES (?, ?, ?, ?, ?)`,
		string(store.ScopeFolder), scopeID, key, value, time.Now().UTC().Format(time.RFC3339),
	); err != nil {
		t.Fatalf("seedLegacyPlaintext: %v", err)
	}
}

func sealTestStore(t *testing.T) *store.Store {
	t.Helper()
	s := testutils.NewRoutdStore(t)
	s.SetSecretKeys([]byte("seal-legacy-test-key"))
	return s
}

// BUGS L16: store.EncryptPlaintextSecrets was correct code with nowhere to run
// — two callers, both in its own test file, no daemon boot path, no CLI verb.
// Rows dashd wrote before encryption-at-rest landed stayed plaintext on disk,
// which is exactly what 5/14 promises they are not.
func TestSealLegacySealsPlaintextRows(t *testing.T) {
	s := sealTestStore(t)
	seedLegacyPlaintext(t, s, "atlas", "A", "aaa")
	seedLegacyPlaintext(t, s, "atlas", "B", "bbb")
	if err := s.SetSecret(store.ScopeFolder, "atlas", "C", "ccc"); err != nil {
		t.Fatal(err)
	}

	var out strings.Builder
	if err := runSealLegacy(s, false, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "sealed 2 of 2") {
		t.Errorf("output must report the count, got: %q", out.String())
	}

	left, err := s.CountPlaintextSecrets(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if left != 0 {
		t.Errorf("%d rows still plaintext after sealing", left)
	}
	// Sealing must not change what a reader sees — including the row that was
	// already sealed and must not have been re-encrypted.
	for key, want := range map[string]string{"A": "aaa", "B": "bbb", "C": "ccc"} {
		got, err := s.GetSecret(store.ScopeFolder, "atlas", key)
		if err != nil {
			t.Fatalf("GetSecret(%s): %v", key, err)
		}
		if got.Value != want {
			t.Errorf("%s reads %q after sealing, want %q", key, got.Value, want)
		}
	}
}

// --dry-run is the whole point of a verb over a boot hook: it reports the blast
// radius and changes nothing, so the run can be rehearsed against a `.backup`
// copy first. A rehearsal that matches 0 rows is a red flag, not an all-clear
// (derived_vs_stored_migration) — which only works if the count is visible.
func TestSealLegacyDryRunCountsAndChangesNothing(t *testing.T) {
	s := sealTestStore(t)
	seedLegacyPlaintext(t, s, "atlas", "A", "aaa")

	var out strings.Builder
	if err := runSealLegacy(s, true, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "1 plaintext secret row(s) would be sealed") {
		t.Errorf("dry run must report the count, got: %q", out.String())
	}
	if !strings.Contains(out.String(), "nothing changed") {
		t.Errorf("dry run must say it changed nothing, got: %q", out.String())
	}

	left, err := s.CountPlaintextSecrets(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if left != 1 {
		t.Errorf("dry run sealed %d row(s); it must change nothing", 1-left)
	}
}

func TestSealLegacyOnACleanTableIsANoOp(t *testing.T) {
	s := sealTestStore(t)
	if err := s.SetSecret(store.ScopeFolder, "atlas", "C", "ccc"); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	if err := runSealLegacy(s, false, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "nothing to seal") {
		t.Errorf("got: %q", out.String())
	}
}

// Without a key the migrator must refuse, not report a silent success: sealing
// is its only job. A caller that cannot tell "nothing to seal" from "no key" is
// how this went unnoticed in the first place.
func TestSealLegacyWithoutAKeyRefuses(t *testing.T) {
	s := testutils.NewRoutdStore(t)

	if _, err := s.EncryptPlaintextSecrets(context.Background()); !errors.Is(err, store.ErrNoSecretsKey) {
		t.Errorf("err = %v, want ErrNoSecretsKey", err)
	}
}

func TestParseSealLegacy(t *testing.T) {
	for _, tc := range []struct {
		name    string
		args    []string
		want    bool
		wantErr bool
	}{
		{"no flags", nil, false, false},
		{"dry run", []string{"--dry-run"}, true, false},
		{"stray positional", []string{"atlas"}, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseSealLegacy("secret seal-legacy", tc.args)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if !tc.wantErr && got != tc.want {
				t.Errorf("dryRun = %v, want %v", got, tc.want)
			}
		})
	}
}
