package store

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
)

func TestTurnWorkReadsOnlyEligibleRows(t *testing.T) {
	s := openTurnEntities(t)
	now := time.Now()
	for i := range 40 {
		if _, err := s.OpenTurnContext(fmt.Sprint(i), "demo", "", "chat", "u", "", nil); err != nil {
			t.Fatal(err)
		}
	}
	write := func(id string, attrs map[string]string) {
		t.Helper()
		if err := s.WriteEntities(t.Context(), nil, objstore.Write{Op: objstore.Update, Entity: TurnContexts,
			Key: objstore.Key{Partition: OwnerRoutd, Sort: id}, Attrs: attrs}); err != nil {
			t.Fatal(err)
		}
	}
	write("0", map[string]string{attrTurnState: TurnPendingRetry, "next_attempt_at": core.TS(now.Add(-time.Second))})
	write("1", map[string]string{attrTurnState: TurnPendingRetry, "next_attempt_at": core.TS(now.Add(time.Hour))})
	write("2", map[string]string{"status_owed": "running"})
	if err := s.SetRunReturned("3"); err != nil {
		t.Fatal(err)
	}
	write("4", map[string]string{AttrTurnStartedAt: core.TS(now.Add(-2 * time.Hour))})
	c := &stateReadCounter{Store: s.obj}
	reader := NewOver(c)
	for _, read := range []struct {
		name, id string
		fn       func() ([]TurnContext, error)
	}{
		{"retry", "0", func() ([]TurnContext, error) { return reader.DeferredTurns(t.Context(), now) }},
		{"status", "2", func() ([]TurnContext, error) { return reader.OwedTurnStatuses(t.Context()) }},
		{"recovery", "3", func() ([]TurnContext, error) { return reader.RecoverableTurns(t.Context()) }},
	} {
		t.Run(read.name, func(t *testing.T) {
			c.rows = 0
			turns, err := read.fn()
			if err != nil || len(turns) != 1 || turns[0].TurnID != read.id || c.rows != 2 {
				t.Fatalf("turns=%v err=%v rows=%d", turns, err, c.rows)
			}
		})
	}
	c.rows = 0
	if n, err := reader.SweepExpiredRunning(time.Hour); err != nil || n != 1 || c.rows > 6 {
		t.Fatalf("expiry=%d err=%v rows=%d", n, err, c.rows)
	}
	if err := s.SetTurnState("0", TurnDone); err != nil {
		t.Fatal(err)
	}
	if err := s.SetTurnState("3", TurnDone); err != nil {
		t.Fatal(err)
	}
	tc, _ := s.TurnContextOf("2")
	if err := s.AcknowledgeTurnStatus(t.Context(), tc); err != nil {
		t.Fatal(err)
	}
	c.rows = 0
	a, e1 := reader.DeferredTurns(t.Context(), now)
	b, e2 := reader.OwedTurnStatuses(t.Context())
	d, e3 := reader.RecoverableTurns(t.Context())
	n, e4 := reader.SweepExpiredRunning(time.Hour)
	if len(a)+len(b)+len(d) != 0 || n != 0 || e1 != nil || e2 != nil || e3 != nil || e4 != nil || c.rows != 0 {
		t.Fatalf("acknowledged work remains: %v %v %v %v rows=%d", e1, e2, e3, e4, c.rows)
	}
}

func TestTurnHistoryReadsOnlyWindow(t *testing.T) {
	s := openTurnEntities(t)
	now := time.Now()
	for i := range 40 {
		at := now.Add(-time.Hour)
		if i == 0 {
			at = now
		}
		if err := s.WriteEntities(context.Background(), nil, objstore.Write{Op: objstore.Put, Entity: TurnArchive,
			Key: objstore.Key{Partition: OwnerRoutd, Sort: fmt.Sprint(i)}, Attrs: map[string]string{
				AttrTurnStartedAt: core.TS(at), attrTurnState: TurnDone,
			}}); err != nil {
			t.Fatal(err)
		}
	}
	c := &stateReadCounter{Store: s.obj}
	turns, err := NewOver(c).TurnsSince(now)
	if err != nil || len(turns) != 1 || turns[0].TurnID != "0" || c.rows != 2 {
		t.Fatalf("history=%v err=%v rows=%d", turns, err, c.rows)
	}
}

type expiryRace struct {
	objstore.Store
	other *Store
	fired bool
}

func (r *expiryRace) GuardedBatch(ctx context.Context, checks []objstore.Check, writes []objstore.Write) (bool, error) {
	if !r.fired {
		r.fired = true
		if err := r.other.WriteEntities(ctx, nil, objstore.Write{Op: objstore.Update, Entity: TurnContexts,
			Key: objstore.Key{Partition: OwnerRoutd, Sort: "race"}, Attrs: map[string]string{AttrTurnStartedAt: core.NowTS()}}); err != nil {
			return false, err
		}
	}
	return r.Store.GuardedBatch(ctx, checks, writes)
}
func TestExpiryKeepsConcurrentRestart(t *testing.T) {
	s := openTurnEntities(t)
	if err := s.WriteEntities(t.Context(), nil, objstore.Write{Op: objstore.Put, Entity: TurnContexts,
		Key: objstore.Key{Partition: OwnerRoutd, Sort: "race"}, Attrs: map[string]string{
			attrTurnState: TurnRunning, AttrTurnStartedAt: core.TS(time.Now().Add(-2 * time.Hour)),
		}}); err != nil {
		t.Fatal(err)
	}
	reader := NewOver(&expiryRace{Store: s.obj, other: s})
	n, err := reader.SweepExpiredRunning(time.Hour)
	tc, _ := s.TurnContextOf("race")
	if err != nil || n != 0 || tc.State != TurnRunning {
		t.Fatalf("fresh turn expired: n=%d err=%v turn=%+v", n, err, tc)
	}
}

func TestTurnIndexMigrationPreservesLegacyWork(t *testing.T) {
	s := openTurnEntities(t)
	now := time.Now()
	for _, entity := range []string{TurnContexts, TurnArchive} {
		ok, err := s.obj.GuardedBatch(t.Context(), nil, []objstore.Write{{Op: objstore.Put, Entity: entity,
			Key: objstore.Key{Partition: OwnerRoutd, Sort: entity}, Attrs: map[string]string{
				attrTurnState: TurnPendingRetry, "next_attempt_at": core.TS(now), AttrTurnStartedAt: core.TS(now),
			}}})
		if err != nil || !ok {
			t.Fatalf("seed legacy: %v %v", ok, err)
		}
	}
	if err := s.MigrateStateIndexes(t.Context()); err != nil {
		t.Fatal(err)
	}
	turns, err := s.DeferredTurns(t.Context(), now)
	history, historyErr := s.TurnsSince(now)
	if err != nil || historyErr != nil || len(turns) != 1 || len(history) != 2 {
		t.Fatalf("lost legacy work: %v %v %v %v", turns, history, err, historyErr)
	}
}
