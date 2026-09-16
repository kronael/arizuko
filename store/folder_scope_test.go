package store

import (
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
