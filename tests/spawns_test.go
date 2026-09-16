package tests

// Spawn counts for the end-to-end stacks. `spawns` is an entity, so a count is
// a read through runed's own storage layer rather than a query naming columns,
// and every body that waits on a run goes through the same two readers.

import (
	"testing"
	"time"

	"github.com/kronael/arizuko/objstore/dynstore/dyntest"
	"github.com/kronael/arizuko/runed"
	"github.com/kronael/arizuko/runed/runstore"
	"github.com/kronael/arizuko/tests/testutils"
)

func TestMain(m *testing.M) { dyntest.Main(m) }

// spawnCount answers how many runs runed has recorded, in any state.
func spawnCount(t *testing.T, db *runed.DB) int {
	t.Helper()
	rows, err := runstore.New(db.Obj()).RecentSpawns(t.Context(), "", "", 1000)
	if err != nil {
		t.Fatalf("read the spawns: %v", err)
	}
	return len(rows)
}

// liveSpawnCount answers how many runs are queued or running.
func liveSpawnCount(t *testing.T, db *runed.DB) int {
	t.Helper()
	n, err := runstore.New(db.Obj()).LiveSpawnCount(t.Context())
	if err != nil {
		t.Fatalf("count the live runs: %v", err)
	}
	return n
}

// waitForSpawn waits until runed has recorded a run at all — the execution
// plane saw a real dispatch rather than a stub.
func waitForSpawn(t *testing.T, db *runed.DB, timeout time.Duration) {
	t.Helper()
	testutils.WaitFor(t, "a recorded spawn", timeout, func() bool {
		return spawnCount(t, db) > 0
	})
}
