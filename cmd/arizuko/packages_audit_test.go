package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kronael/arizuko/store"

	"github.com/kronael/arizuko/audit"
)

// A package apply mutates many rows at once, so it needs a verdict of its own:
// ONE audit row for the bundle, or N for the grants? It is now ONE — install
// goes through `arizuko apply`, whose contract is one row per apply (spec 5/8
// §"CAS implementation" (3)), and a second per-row trail beside it would be the
// parallel path BUGS L44 exists to delete.
//
// That only stays honest if the one row NAMES what it wrote. BUGS.md Q5 refused
// a bundle row because it would say "installed grantpkg" and hide that the
// install handed @ops admin on a folder; the answer is not N rows, it is a row
// carrying the row IDENTITIES (resreg.ApplyOpts.Extra). An acl PK is
// principal|action|scope|params|predicate|effect, so for the grant half the
// identity IS the grant. Each acl row also carries granted_by=package:<name>,
// so the provenance survives in the table too.
//
// Identities, not whole rows: audit.marshalParams caps params_summary at 512
// bytes, and a row-valued summary collapsed to {"_truncated":true}, which
// answers nothing at all.
//
// Falsifiable: drop ApplyOpts.Extra from emitApplyAudit and the rows still land
// and the count case still passes, but the scope cases fail — the whole
// difference between a trail that is useful and one that is merely truthful.
// Drop cliActor() and the actor assertion fails.

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

// installGrantPkg installs a two-grant package as osUser and returns the store dir.
func installGrantPkg(t *testing.T, osUser string) *store.Store {
	t.Helper()
	base := t.TempDir()
	t.Setenv("ARIZUKO_DATA_DIR", base)
	t.Setenv("USER", osUser)
	st := instanceStore(t)
	src := filepath.Join(base, "grantpkg")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "grantpkg.yml"),
		[]byte("services:\n  grantpkg:\n    image: x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "grantpkg.yaml"), []byte(
		"acl:\n"+
			"  - principal: '@ops'\n    action: reply\n    scope: eng/*\n    effect: allow\n"+
			"  - principal: '@ops'\n    action: admin\n    scope: eng/tts\n    effect: allow\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmdPackages([]string{"gaudit", "install", src, "--folder", "eng/**"})
	return st
}

// One row for the apply, and it names every grant it handed out. A count alone
// would hide that the install gave @ops admin on eng/tts.
func TestPackageGrantsAuditedInOneApplyRow(t *testing.T) {
	s := installGrantPkg(t, "alice")

	rows := aclAuditRows(t, s, "config.apply")
	if len(rows) != 1 {
		t.Fatalf("config.apply rows = %d, want 1 (packages install goes through apply)", len(rows))
	}
	if rows[0][0] != "cli:alice" || rows[0][1] != "cli" {
		t.Errorf("actor/surface = (%q, %q), want (cli:alice, cli)", rows[0][0], rows[0][1])
	}
	for _, want := range []string{"@ops|reply|eng/*", "@ops|admin|eng/tts"} {
		if !strings.Contains(rows[0][2], want) {
			t.Errorf("params_summary does not name %s: %q", want, rows[0][2])
		}
	}
}

// The row must also say the grants came from a package rather than from an
// operator typing `arizuko grant` — the actor is the same person either way.
func TestPackageGrantsAuditNamesPackage(t *testing.T) {
	s := installGrantPkg(t, "alice")

	rows := aclAuditRows(t, s, "config.apply")
	if len(rows) != 1 {
		t.Fatalf("config.apply rows = %d, want 1", len(rows))
	}
	if !strings.Contains(rows[0][2], `"package":"grantpkg"`) {
		t.Errorf("params_summary = %q, want the package name", rows[0][2])
	}
	// The provenance also survives in the table the grants landed in, which is
	// what an operator reading `acl` sees without joining audit_log.
	rows2, err := s.ListACL("@ops")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows2) != 2 {
		t.Fatalf("acl rows for @ops = %d, want 2", len(rows2))
	}
	for _, r := range rows2 {
		if r.GrantedBy != "package:grantpkg" {
			t.Errorf("acl row %+v granted_by = %q, want package:grantpkg", r, r.GrantedBy)
		}
	}
}

// Withdrawal records too, through the same engine: a second config.apply row
// naming the package. A trail that shows authority landing and never leaving
// reads as "still granted" for a grant that is gone.
func TestPackageGrantsRemovalAudited(t *testing.T) {
	t.Cleanup(func() { dropInstance(t) })
	s := installGrantPkg(t, "alice")
	cmdPackages([]string{"gaudit", "remove", "grantpkg"})

	rows := aclAuditRows(t, s, "config.apply")
	if len(rows) != 2 {
		t.Fatalf("config.apply rows = %d, want 2 (install + remove)", len(rows))
	}
	if rows[1][0] != "cli:alice" || rows[1][1] != "cli" {
		t.Errorf("removal actor/surface = (%q, %q), want (cli:alice, cli)", rows[1][0], rows[1][1])
	}
	if !strings.Contains(rows[1][2], `"removed"`) || !strings.Contains(rows[1][2], `"package":"grantpkg"`) {
		t.Errorf("removal params_summary = %q, want the package and what it withdrew", rows[1][2])
	}
}
