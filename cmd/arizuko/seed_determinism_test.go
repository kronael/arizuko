package main

import (
	"testing"
	"time"

	"github.com/kronael/arizuko/resreg"
)

// BUGS Z5: `0005-network-rules.sql` seeded its two default rows with
// CURRENT_TIMESTAMP, so two independently-bootstrapped routd.db files whose
// migration runs crossed a wall-clock second got different `created_at` values
// — and `created_at` is inside resreg.Export's projection, which
// resreg.Checksum hashes verbatim. Identical operator intent therefore produced
// permanently unequal content hashes, and a no-force cross-instance `apply`
// could never match. Every existing cross-instance test used `--force`, which
// skips the checksum compare, so nothing caught it.
//
// The guard is on the SEED, not on the hash: Checksum must stay sha256 of the
// exact bytes Export emits (one renderer, spec 5/8).
func TestFreshInstancesAgreeOnSeedChecksum(t *testing.T) {
	_, a := openInstance(t)
	// Cross a wall-clock second so a CURRENT_TIMESTAMP seed is guaranteed to
	// differ. Without this the two bootstraps land in the same second and the
	// bug hides — which is exactly why it survived until a 50-run sweep.
	time.Sleep(1100 * time.Millisecond)
	_, b := openInstance(t)

	sumA, err := resreg.Checksum(resreg.OnDB(a[resreg.SubsystemRoutd].DB()), resreg.SubsystemRoutd)
	if err != nil {
		t.Fatal(err)
	}
	sumB, err := resreg.Checksum(resreg.OnDB(b[resreg.SubsystemRoutd].DB()), resreg.SubsystemRoutd)
	if err != nil {
		t.Fatal(err)
	}
	if sumA != sumB {
		t.Errorf("two fresh instances disagree on the routd checksum:\n  a = %s\n  b = %s", sumA, sumB)
	}
}

// The seed stamp is a fixed literal, so assert the value directly too: a future
// migration re-seeding these rows with CURRENT_TIMESTAMP would pass the
// checksum test only while both bootstraps stayed inside one second.
func TestNetworkRulesSeedStampIsLiteral(t *testing.T) {
	_, stores := openInstance(t)
	rows, err := stores[resreg.SubsystemRoutd].DB().Query(
		`SELECT target, created_at FROM network_rules WHERE folder = '' AND created_by = 'system' ORDER BY target`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := map[string]string{}
	for rows.Next() {
		var target, createdAt string
		if err := rows.Scan(&target, &createdAt); err != nil {
			t.Fatal(err)
		}
		got[target] = createdAt
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"anthropic.com":     "2026-08-04T00:00:00Z",
		"api.anthropic.com": "2026-08-04T00:00:00Z",
	}
	if len(got) != len(want) {
		t.Fatalf("seed rows = %v, want %v", got, want)
	}
	for target, stamp := range want {
		if got[target] != stamp {
			t.Errorf("network_rules[%s].created_at = %q, want %q", target, got[target], stamp)
		}
	}
}
