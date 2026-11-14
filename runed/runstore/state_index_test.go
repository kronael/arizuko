package runstore

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/objstore/dynstore/dyntest"
)

type spawnCounter struct {
	objstore.Store
	rows int
}

func (c *spawnCounter) Query(ctx context.Context, q objstore.Query) ([]objstore.Item, error) {
	m := q
	m.Where, m.Project = nil, nil
	rows, err := c.Store.Query(ctx, m)
	if err != nil {
		return nil, err
	}
	c.rows += len(rows)
	return c.Store.Query(ctx, q)
}
func spawnFixture(t *testing.T) (*Store, *spawnCounter) {
	t.Helper()
	raw := dyntest.Store(t)
	if err := EnsureEntities(t.Context(), raw); err != nil {
		t.Fatal(err)
	}
	c := &spawnCounter{Store: raw}
	return New(c), c
}
func TestSpawnWorkReadsOnlyEligibleRows(t *testing.T) {
	s, c := spawnFixture(t)
	now := time.Now()
	for i := range 40 {
		at, state := now, "exited"
		if i == 0 {
			at = now.Add(-8 * 24 * time.Hour)
		}
		if i == 1 {
			state = "running"
		}
		ok, err := s.s.GuardedBatch(t.Context(), nil, []objstore.Write{{Op: objstore.Put, Entity: Spawns, Key: spawnKey(fmt.Sprint(i)),
			Attrs: map[string]string{attrCreatedAt: core.TS(at), attrState: state, attrKind: KindAgent, attrFolder: "demo"}}})
		if err != nil || !ok {
			t.Fatalf("seed: %v %v", ok, err)
		}
	}
	c.rows = 0
	if n, err := s.LiveSpawnCount(t.Context()); err != nil || n != 1 || c.rows != 1 {
		t.Fatalf("live=%d err=%v rows=%d", n, err, c.rows)
	}
	c.rows = 0
	if n, err := s.DropSpawnsBefore(t.Context(), now.Add(-7*24*time.Hour)); err != nil || n != 1 || c.rows > 3 {
		t.Fatalf("expired=%d err=%v rows=%d", n, err, c.rows)
	}
	if err := s.EndSpawn(t.Context(), "1", "exited", "ok", 0); err != nil {
		t.Fatal(err)
	}
	c.rows = 0
	if n, err := s.ExpireOrphans(t.Context()); err != nil || n != 0 || c.rows != 0 {
		t.Fatalf("orphan=%d err=%v rows=%d", n, err, c.rows)
	}
	if n, err := s.LiveAgentSpawnCount(t.Context()); err != nil || n != 0 || c.rows != 0 {
		t.Fatalf("active=%d err=%v rows=%d", n, err, c.rows)
	}
}
func TestSpawnIndexMigrationRunsOnce(t *testing.T) {
	s, c := spawnFixture(t)
	ok, err := c.Store.GuardedBatch(t.Context(), nil, []objstore.Write{{Op: objstore.Put, Entity: Spawns, Key: spawnKey("legacy"),
		Attrs: map[string]string{attrState: "running", attrKind: KindAgent, attrCreatedAt: core.TS(time.Now().Add(-8 * 24 * time.Hour))}}})
	if err != nil || !ok {
		t.Fatalf("seed: %v %v", ok, err)
	}
	if err := s.MigrateStateIndexes(t.Context()); err != nil {
		t.Fatal(err)
	}
	if n, err := s.LiveSpawnCount(t.Context()); err != nil || n != 1 {
		t.Fatalf("legacy live=%d err=%v", n, err)
	}
	if n, err := s.DropSpawnsBefore(t.Context(), time.Now().Add(-7*24*time.Hour)); err != nil || n != 1 {
		t.Fatalf("legacy expiry=%d err=%v", n, err)
	}
	c.rows = 0
	if err := s.MigrateStateIndexes(t.Context()); err != nil || c.rows != 1 {
		t.Fatalf("migration=%v rows=%d", err, c.rows)
	}
}

type spawnRefresh struct {
	objstore.Store
	other *Store
	fired bool
}

func (r *spawnRefresh) GuardedBatch(ctx context.Context, checks []objstore.Check, writes []objstore.Write) (bool, error) {
	if !r.fired {
		r.fired = true
		if _, err := r.other.s.GuardedBatch(ctx, nil, []objstore.Write{{Op: objstore.Update, Entity: Spawns, Key: spawnKey("race"), Attrs: map[string]string{attrCreatedAt: core.NowTS()}}}); err != nil {
			return false, err
		}
	}
	return r.Store.GuardedBatch(ctx, checks, writes)
}
func TestSpawnExpiryKeepsConcurrentReplacement(t *testing.T) {
	s, c := spawnFixture(t)
	if _, err := s.s.GuardedBatch(t.Context(), nil, []objstore.Write{{Op: objstore.Put, Entity: Spawns, Key: spawnKey("race"), Attrs: map[string]string{attrCreatedAt: core.TS(time.Now().Add(-8 * 24 * time.Hour)), attrState: "running"}}}); err != nil {
		t.Fatal(err)
	}
	race := New(&spawnRefresh{Store: c.Store, other: s})
	_, _ = race.DropSpawnsBefore(t.Context(), time.Now().Add(-7*24*time.Hour))
	if _, err := s.SpawnByRunID(t.Context(), "race"); err != nil {
		t.Fatal("deleted replacement:", err)
	}
}
