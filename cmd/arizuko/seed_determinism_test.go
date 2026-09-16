package main

import (
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
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

	sumA, err := resreg.Checksum(resreg.On(a[resreg.SubsystemRoutd].Obj()), resreg.SubsystemRoutd)
	if err != nil {
		t.Fatal(err)
	}
	sumB, err := resreg.Checksum(resreg.On(b[resreg.SubsystemRoutd].Obj()), resreg.SubsystemRoutd)
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
	rules, err := stores[resreg.SubsystemRoutd].ListNetworkRules("")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, r := range rules {
		if r.CreatedBy != "system" {
			continue
		}
		got[r.Target] = core.TS(r.CreatedAt)
	}
	// The stamp is core.TS of a fixed instant, because a fresh instance's config
	// checksum has to be the same value on every machine.
	stamp := core.TS(time.Date(2026, 8, 4, 0, 0, 0, 0, time.UTC))
	want := map[string]string{"anthropic.com": stamp, "api.anthropic.com": stamp}
	if len(got) != len(want) {
		t.Fatalf("seed rows = %v, want %v", got, want)
	}
	for target, at := range want {
		if got[target] != at {
			t.Errorf("network_rules[%s].created_at = %q, want %q", target, got[target], at)
		}
	}
}
