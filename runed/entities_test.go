package runed

// The entity set a fresh runed.db serves, which is what replaced the migration
// chain's drops.
//
// `spawn_logs` and `mcp_tokens` were created by the first migration and retired
// by two later ones because neither ever got a writer, and a test watched the
// drops keep applying. There is no chain to watch now: an entity exists because
// runstore declares it, so the same guarantee is the DECLARATION — a retired
// name is not in it, and a name that is in it answers.

import (
	"testing"

	"github.com/kronael/arizuko/audit"
	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/runed/runstore"
)

// TestEntitiesAreExactlyWhatRunedReads: every declared entity answers on a fresh
// database, the two retired tables answer nothing, and nothing else is
// declared. An entity missing from the opener is BUGS F29 — every write dropped
// while the daemon reports healthy — and an entity that is declared but unread
// is the empty table a schema reader mistakes for a feature.
func TestEntitiesAreExactlyWhatRunedReads(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatalf("fresh open: %v", err)
	}
	defer db.Close()

	want := []string{runstore.Spawns, runstore.SessionLog, runstore.CircuitBreaker}
	if len(runstore.Entities) != len(want) {
		t.Fatalf("runstore declares %v, want exactly %v", runstore.Entities, want)
	}
	for i, name := range want {
		if runstore.Entities[i] != name {
			t.Errorf("entity %d is %q, want %q", i, runstore.Entities[i], name)
		}
	}

	obj := db.Obj()
	// audit's entity rides in this database too, and the opener applies it.
	for _, entity := range append(append([]string{}, runstore.Entities...), audit.Entity) {
		if _, err := obj.Query(t.Context(), objstore.Query{
			Entity: entity, Partition: "any",
		}); err != nil {
			t.Errorf("the opener left %s unservable: %v", entity, err)
		}
	}

	// The retired names are not entities, so a read against one fails rather
	// than answering an empty set that reads as "no spawn output yet".
	for _, gone := range []string{"spawn_logs", "mcp_tokens"} {
		if _, err := obj.Query(t.Context(), objstore.Query{
			Entity: gone, Partition: "any",
		}); err == nil {
			t.Errorf("%s answers a read; it was retired for having no writer", gone)
		}
	}
}
