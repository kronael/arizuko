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
