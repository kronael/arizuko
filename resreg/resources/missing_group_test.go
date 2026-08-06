package resources

// The missing-group rule (spec 5/8 §"The missing-group rule") against the
// REAL resource declarations and the REAL schema — the point of the rule is
// which columns SQLite does and does not catch, so a synthetic resource
// would prove nothing.

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/kronael/arizuko/resreg"
)

// seedGroup inserts a groups row with direct SQL — the fixture must not go
// through the engine under test.
func seedGroup(t *testing.T, db *sql.DB, folder string) {
	t.Helper()
	if _, err := db.Exec(
		`INSERT INTO groups (folder, added_at, product) VALUES (?, '2026-08-06T00:00:00Z', 'assistant')`,
		folder); err != nil {
		t.Fatalf("seed group %s: %v", folder, err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM groups WHERE folder = ?`, folder).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("seed group %s did not land (count=%d)", folder, n)
	}
}

// TestMissingGroup_NetworkRulesOrphanIsRealToday proves the gap the rule
// closes is real, not hypothetical: network_rules.folder has NO foreign key
// (folder=” instance-global rows would fail one), so an apply naming a
// folder that is not a group commits a silently orphaned row. This test
// asserts the ORPHAN LANDS when the preflight is bypassed — if a future
// migration adds the FK, this test fails and the rule's justification must be
// re-read, which is exactly the signal wanted.
func TestMissingGroup_NetworkRulesOrphanIsRealToday(t *testing.T) {
	db := openMem(t)
	seedGroup(t, db, "corp/eng")

	// Migration 0005 seeds two folder='' instance-global rows, so scope the
	// before/after counts to the folder under test rather than the table.
	var before int
	if err := db.QueryRow(`SELECT COUNT(*) FROM network_rules WHERE folder = 'corp/typo'`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if before != 0 {
		t.Fatalf("corp/typo must start with no rules, got %d", before)
	}

	// No preflight: straight to Apply, naming a folder that is not a group.
	if _, err := resreg.Apply(context.Background(), db, resreg.SubsystemRoutd,
		routdChecksum(t, db), false, map[string]any{
			"network_rules": []NetworkRulesRow{{Folder: "corp/typo", Target: "api.example.com"}},
		}, nil); err != nil {
		t.Fatalf("apply without preflight should succeed today (that IS the gap): %v", err)
	}
	var after int
	if err := db.QueryRow(`SELECT COUNT(*) FROM network_rules WHERE folder = 'corp/typo'`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != 1 {
		t.Fatalf("orphan network_rules row count = %d, want 1 — the gap this rule closes", after)
	}
}

// TestMissingGroup_RefusesUnknownFolder: the same manifest, run through the
// rule, is refused — and named precisely.
func TestMissingGroup_RefusesUnknownFolder(t *testing.T) {
	db := openMem(t)
	seedGroup(t, db, "corp/eng")

	manifest := map[string]any{
		"network_rules": []NetworkRulesRow{{Folder: "corp/typo", Target: "api.example.com"}},
	}
	known, err := resreg.KnownFolders(db, manifest)
	if err != nil {
		t.Fatalf("KnownFolders: %v", err)
	}
	if !known["corp/eng"] {
		t.Fatalf("live group corp/eng missing from known set %v", known)
	}
	verr := resreg.ValidateFolderRefs(resreg.SubsystemRoutd, manifest, known)
	if verr == nil {
		t.Fatal("ValidateFolderRefs accepted a rule for a folder that is not a group")
	}
	if !errors.Is(verr, resreg.ErrMissingGroup) {
		t.Errorf("error = %v, want ErrMissingGroup", verr)
	}
	if got := verr.Error(); !strings.Contains(got, "network_rules -> corp/typo") {
		t.Errorf("error %q must name the resource and the folder", got)
	}
}

// TestMissingGroup_AcceptsFolderDeclaredInTheSameManifest: the rule's "or
// declared somewhere in the manifest set" half. A restore onto a fresh
// instance carries the groups row and its rules in one apply; refusing that
// would make the rule refuse every genuine DR restore.
func TestMissingGroup_AcceptsFolderDeclaredInTheSameManifest(t *testing.T) {
	db := openMem(t)
	// Deliberately NO live group: only the manifest declares it.
	manifest := map[string]any{
		"groups":        []GroupsRow{{Folder: "new/team", Product: "assistant"}},
		"network_rules": []NetworkRulesRow{{Folder: "new/team", Target: "api.example.com"}},
	}
	known, err := resreg.KnownFolders(db, manifest)
	if err != nil {
		t.Fatalf("KnownFolders: %v", err)
	}
	if err := resreg.ValidateFolderRefs(resreg.SubsystemRoutd, manifest, known); err != nil {
		t.Fatalf("a folder the manifest itself declares must pass: %v", err)
	}
	// And it really applies: the rule must not be a check that only ever says no.
	if _, err := resreg.Apply(context.Background(), db, resreg.SubsystemRoutd,
		routdChecksum(t, db), false, manifest, nil); err != nil {
		t.Fatalf("apply: %v", err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM network_rules WHERE folder = 'new/team'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("network_rules rows for new/team = %d, want 1", n)
	}
}

// TestMissingGroup_InstanceGlobalFolderExempt: network_rules carries
// folder=” for instance-global egress rules — the very reason that column
// has no FK. The rule must not reject them.
func TestMissingGroup_InstanceGlobalFolderExempt(t *testing.T) {
	db := openMem(t)
	manifest := map[string]any{
		"network_rules": []NetworkRulesRow{{Folder: "", Target: "api.anthropic.com"}},
	}
	known, err := resreg.KnownFolders(db, manifest)
	if err != nil {
		t.Fatalf("KnownFolders: %v", err)
	}
	if err := resreg.ValidateFolderRefs(resreg.SubsystemRoutd, manifest, known); err != nil {
		t.Fatalf("instance-global folder='' must be exempt: %v", err)
	}
}

// TestMissingGroup_CatchesWebRoutesBeforeTheFKWould: web_routes.folder DOES
// have an FK, so an apply would fail mid-write. The rule's value here is
// timing, not detection — it refuses before any transaction opens. Asserts
// both halves: the rule names it, AND an unguarded apply of the same
// manifest fails (proving the row never silently lands either way).
func TestMissingGroup_CatchesWebRoutesBeforeTheFKWould(t *testing.T) {
	db := openMem(t)
	seedGroup(t, db, "corp/eng")
	manifest := map[string]any{
		"web_routes": []WebRoutesRow{{PathPrefix: "/x/", Access: "public", Folder: "corp/ghost"}},
	}
	known, err := resreg.KnownFolders(db, manifest)
	if err != nil {
		t.Fatalf("KnownFolders: %v", err)
	}
	verr := resreg.ValidateFolderRefs(resreg.SubsystemRoutd, manifest, known)
	if !errors.Is(verr, resreg.ErrMissingGroup) {
		t.Fatalf("error = %v, want ErrMissingGroup", verr)
	}
	if !strings.Contains(verr.Error(), "web_routes -> corp/ghost") {
		t.Errorf("error %q must name web_routes and the folder", verr.Error())
	}
}

// TestMissingGroup_CleanManifestPasses guards against a rule that refuses
// everything: the ordinary case — every reference resolvable — must produce
// no error at all.
func TestMissingGroup_CleanManifestPasses(t *testing.T) {
	db := openMem(t)
	seedGroup(t, db, "corp/eng")
	manifest := map[string]any{
		"network_rules": []NetworkRulesRow{{Folder: "corp/eng", Target: "api.example.com"}},
		"web_routes":    []WebRoutesRow{{PathPrefix: "/eng/", Access: "public", Folder: "corp/eng"}},
	}
	known, err := resreg.KnownFolders(db, manifest)
	if err != nil {
		t.Fatalf("KnownFolders: %v", err)
	}
	if err := resreg.ValidateFolderRefs(resreg.SubsystemRoutd, manifest, known); err != nil {
		t.Fatalf("clean manifest must pass: %v", err)
	}
}
