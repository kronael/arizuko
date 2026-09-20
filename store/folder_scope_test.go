package store

import (
	"github.com/kronael/arizuko/core"
	"sort"
	"strings"
	"testing"
)

// TestFolderScope_ClassifiesEveryDeclaredEntity is what makes "the purge is
// complete" checkable rather than asserted. Every entity carries a verdict:
// purged, kept with a reason, or named as an unpurged folder coordinate. An
// entity joining an owner's declaration arrives here first, so the question of
// whether it confers on a folder cannot be skipped the way it was for acl
// (BUGS L198).
//
// The DECLARATION is the source, not a live store's table list: EntitiesByOwner
// is what every owner's boot makes, so an entity added there is covered the
// moment it is written, with no instance to walk and no boot to wait for.
func TestFolderScope_ClassifiesEveryDeclaredEntity(t *testing.T) {
	var missing []string
	seen := map[string]bool{}
	for _, owner := range []string{OwnerRoutd, OwnerOnbod} {
		for _, entity := range EntitiesByOwner[owner] {
			if seen[entity] {
				continue
			}
			seen[entity] = true
			if !FolderScopeClassified(entity) {
				missing = append(missing, entity)
			}
		}
	}
	// audit's record and the version marker are declared by their own packages
	// and land in every owner, so they are asked for by name.
	for _, entity := range []string{"audit_log", ScopeEntity} {
		if !FolderScopeClassified(entity) {
			missing = append(missing, entity)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("entities with no folder-scope verdict: %s\n"+
			"add each to entityScopePurge, folderScopeKeep, folderScopeUnpurged or "+
			"folderPartitionPurge in store/folder_scope.go",
			strings.Join(missing, ", "))
	}
}

// The twin question, and the half BUGS L302 records: a verdict for an entity no
// owner declares is a rule about a table that is not there, and it hides the
// entity it was renamed from. Another owner's entity is named ON PURPOSE, so it
// is enumerated here rather than derived.
func TestFolderScope_ClassifiesNothingUndeclared(t *testing.T) {
	// authd's and runed's, carried deliberately: the purge is one batch on
	// routd's tables and reaches no other owner, so "no" is the answer a reader
	// needs stated.
	declared := map[string]bool{
		"audit_log": true, ScopeEntity: true,
		"auth_users": true, "session_log": true,
	}
	for _, owner := range []string{OwnerRoutd, OwnerOnbod} {
		for _, entity := range EntitiesByOwner[owner] {
			declared[entity] = true
		}
	}
	var orphans []string
	for table := range folderScopeKeep {
		if !declared[table] {
			orphans = append(orphans, table)
		}
	}
	for table := range folderScopeUnpurged {
		if !declared[table] {
			orphans = append(orphans, table)
		}
	}
	for _, e := range entityScopePurge {
		if !declared[e.entity] {
			orphans = append(orphans, e.entity)
		}
	}
	for _, table := range folderPartitionPurge {
		if !declared[table] {
			orphans = append(orphans, table)
		}
	}
	sort.Strings(orphans)
	if len(orphans) > 0 {
		t.Fatalf("folder-scope verdicts for entities no owner declares: %s\n"+
			"delete each, or name it above with the owner it belongs to",
			strings.Join(orphans, ", "))
	}
}

// A delete whose groups row is already gone still sweeps the orphan rows under
// it, and now REPORTS them. Counting only the `groups` rows made that sweep
// indistinguishable from a no-op, so the CLI died "no group at X" and dashd
// answered 404 — both telling the operator to go and find rows that were
// already deleted (BUGS L323).
func TestDeleteGroupReportsTheOrphansItSweeps(t *testing.T) {
	s := openMem(t)
	if err := s.PutACLRow(core.ACLRow{
		Principal: "alice@x", Action: "admin", Scope: "ghost",
		Effect: EffectAllow, GrantedBy: "test",
	}); err != nil {
		t.Fatalf("seed the orphan grant: %v", err)
	}
	if _, ok := s.GroupByFolder("ghost"); ok {
		t.Fatal("a groups row exists; this test checks nothing")
	}
	if pre, _ := s.ListACL("alice@x"); len(pre) == 0 {
		t.Fatal("the seed did not land; this test checks nothing")
	}

	removed, swept, err := s.DeleteGroup("ghost")
	if err != nil {
		t.Fatalf("DeleteGroup: %v", err)
	}

	// The WORK happened.
	rows, err := s.ListACL("alice@x")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.Scope == "ghost" {
			t.Errorf("the orphan grant survived the purge: %+v", r)
		}
	}
	// And the REPORT says so. `removed` stays 0 — there was no groups row — so
	// `swept` is the only thing that separates this from a folder that never
	// existed, which is exactly what the callers branch on.
	if removed != 0 {
		t.Errorf("removed = %d, want 0: there was no groups row to delete", removed)
	}
	if swept == 0 {
		t.Error("swept = 0, but the orphan grant is gone — a caller cannot tell " +
			"this sweep from a folder that never existed")
	}
}

// The counterpart, and the one that keeps the report honest: a folder with
// nothing under it reports nothing, so `swept > 0` still means work happened.
func TestDeleteGroupReportsNothingForAFolderThatNeverExisted(t *testing.T) {
	s := openMem(t)
	removed, swept, err := s.DeleteGroup("no-such-folder")
	if err != nil {
		t.Fatalf("DeleteGroup: %v", err)
	}
	if removed != 0 || swept != 0 {
		t.Errorf("removed = %d, swept = %d, want 0 and 0", removed, swept)
	}
}

// An empty folder names the subtree of everything, so it is the one argument
// the sweep must refuse.
func TestDeleteGroupRefusesAnEmptyFolder(t *testing.T) {
	s := openMem(t)
	removed, _, err := s.DeleteGroup("")
	if err != nil {
		t.Fatalf("an empty folder should be swallowed, not an error: %v", err)
	}
	if removed != 0 {
		t.Errorf("removed %d for an empty folder", removed)
	}
}
