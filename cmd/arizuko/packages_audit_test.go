package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kronael/arizuko/audit"
	"github.com/kronael/arizuko/resreg"
	"github.com/kronael/arizuko/store"
)

// A package apply mutates many rows at once, so it needs a verdict of its own:
// ONE audit row for the bundle, or N for the rules? It is ONE — install goes
// through `arizuko apply`, whose contract is one row per apply (spec
// 5/17-openapi-mcp §"Audit contract"), and a second per-row trail beside it
// would be the parallel path BUGS L44 exists to delete.
//
// That only stays honest if the one row NAMES what it wrote: the row
// IDENTITIES (resreg.ApplyOpts.Extra), because a count would say "installed
// rulepkg" and hide which egress the install opened. Identities, not whole rows:
// audit.marshalParams caps params_summary at 512 bytes, and a row-valued summary
// collapsed to {"_truncated":true}, which answers nothing at all.
//
// Falsifiable: drop ApplyOpts.Extra from emitApplyAudit and the rows still land
// but the identity cases fail. Drop cliActor() and the actor assertion fails.

// aclAuditRows returns (actor, surface, params_summary) of every audit_log row
// for action in dir/routd.db, oldest first.
func aclAuditRows(t *testing.T, st *store.Store, action string) [][3]string {
	t.Helper()
	rows, err := audit.Query(t.Context(), st.Obj(), audit.Filter{Limit: audit.MaxLimit})
	if err != nil {
		t.Fatalf("read the audit trail: %v", err)
	}
	var out [][3]string
	// Oldest first, which is the order the assertions read.
	for i := len(rows) - 1; i >= 0; i-- {
		if rows[i].Action == action {
			out = append(out, [3]string{rows[i].Actor, rows[i].Surface, rows[i].ParamsSummary})
		}
	}
	return out
}

// installRulePkg installs a two-rule package as osUser and answers the instance
// store it landed in.
func installRulePkg(t *testing.T, osUser string) *store.Store {
	t.Helper()
	seedSyncInstance(t, "gaudit", nil, nil)
	t.Setenv("USER", osUser)
	st := instanceStore(t)
	h, err := store.Handle(store.OwnerOnbod)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PrepareOwner(context.Background(), h, store.OwnerOnbod); err != nil {
		t.Fatal(err)
	}
	if err := resreg.EnsureSubsystem(context.Background(), h, h, resreg.SubsystemOnbod); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(t.TempDir(), "rulepkg")
	mustWrite(t, filepath.Join(src, "rulepkg.yml"), "services:\n  rulepkg:\n    image: x\n")
	mustWrite(t, filepath.Join(src, "arizuko.yml"), "name: rulepkg\n"+
		"network_rules:\n  - folder: ''\n    target: a.example\n  - folder: ''\n    target: b.example\n")
	packageRuns(t, "gaudit", "add", src)
	return st
}

// One row for the apply, and it names every rule it opened. A count alone
// would hide which egress the install handed out.
func TestPackageRulesAuditedInOneApplyRow(t *testing.T) {
	s := installRulePkg(t, "alice")

	rows := aclAuditRows(t, s, "config.apply")
	if len(rows) != 1 {
		t.Fatalf("config.apply rows = %d, want 1 (packages install goes through apply)", len(rows))
	}
	if rows[0][0] != "cli:alice" || rows[0][1] != "cli" {
		t.Errorf("actor/surface = (%q, %q), want (cli:alice, cli)", rows[0][0], rows[0][1])
	}
	for _, want := range []string{"a.example", "b.example"} {
		if !strings.Contains(rows[0][2], want) {
			t.Errorf("params_summary does not name %s: %q", want, rows[0][2])
		}
	}
}

// The row must also say the rules came from a package rather than from an
// operator typing `arizuko network allow` — the actor is the same person either way.
func TestPackageRulesAuditNamesPackage(t *testing.T) {
	s := installRulePkg(t, "alice")

	rows := aclAuditRows(t, s, "config.apply")
	if len(rows) != 1 {
		t.Fatalf("config.apply rows = %d, want 1", len(rows))
	}
	if !strings.Contains(rows[0][2], `"package":"rulepkg"`) {
		t.Errorf("params_summary = %q, want the package name", rows[0][2])
	}
}

// Withdrawal records too, through the same engine: a second config.apply row
// naming the package. A trail that shows egress opening and never closing
// reads as "still open" for a rule that is gone.
