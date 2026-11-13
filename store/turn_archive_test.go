package store

import (
	"context"
	"maps"
	"sync"
	"testing"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
)

func liveTurn(t *testing.T, s *Store, turnID string) (objstore.Item, bool) {
	t.Helper()
	key, _ := turnKey(turnID)
	it, ok, err := s.itemAt(t.Context(), TurnContexts, key)
	if err != nil {
		t.Fatal(err)
	}
	return it, ok
}

func archivedTurn(t *testing.T, s *Store, turnID string) (objstore.Item, bool) {
	t.Helper()
	key, _ := turnKey(turnID)
	it, ok, err := s.itemAt(t.Context(), TurnArchive, key)
	if err != nil {
		t.Fatal(err)
	}
	return it, ok
}

// A turn that finishes leaves turn_context in the write that finishes it and
// lands in turn_archive with every attribute it carried, so the live reads
// never see it and a read by id still does.
func TestFinishedTurnMovesToTheArchiveWhole(t *testing.T) {
	s := openTurnEntities(t)
	if _, err := s.OpenTurnContext("t1", "demo", "topic", "tg:1", "u1", "tg:origin",
		&TurnAdmission{InputLast: "cursor"}); err != nil {
		t.Fatal(err)
	}
	for _, set := range []error{
		s.SetTurnRunID("t1", "run-1"), s.SetTurnGatewayKey("t1", "sha256:k"),
		s.SetTurnTriggerMsg("t1", "pm-1"), s.SetRunReturned("t1"),
	} {
		if set != nil {
			t.Fatal(set)
		}
	}
	before, _ := liveTurn(t, s, "t1")
	if err := s.SetTurnState("t1", TurnDone); err != nil {
		t.Fatal(err)
	}
	if _, ok := liveTurn(t, s, "t1"); ok {
		t.Fatal("the finished turn is still in turn_context")
	}
	archived, ok := archivedTurn(t, s, "t1")
	want := maps.Clone(before.Attrs)
	want[attrTurnState] = TurnDone
	if !ok || !maps.Equal(archived.Attrs, want) {
		t.Fatalf("archive holds %v, want %v", archived.Attrs, want)
	}
	tc, ok := s.TurnContextOf("t1")
	if !ok || tc.State != TurnDone || tc.ReturnTo != "tg:origin" {
		t.Fatalf("by id: %+v ok=%v", tc, ok)
	}
	if hash, err := s.TurnGatewayKey("t1"); err != nil || hash != "sha256:k" {
		t.Fatalf("gateway key of an archived turn: %q %v", hash, err)
	}
	running, err := s.RunningTurns()
	if err != nil || len(running) != 0 {
		t.Fatalf("running turns = %v err %v", running, err)
	}
	since, err := s.TurnsSince(tc.StartedAt)
	if err != nil || len(since) != 1 || since[0].TurnID != "t1" {
		t.Fatalf("TurnsSince = %+v err %v", since, err)
	}
	if live, err := s.OpenTurnContext("t1", "demo", "", "tg:1", "u1", "", nil); err != nil || live {
		t.Fatalf("an archived done turn opened again: live=%v err=%v", live, err)
	}
}

// A done turn whose run is still out stays live: a stop and the run's own
// return still write it.
func TestDoneTurnStaysLiveUntilItsRunReturns(t *testing.T) {
	s := openTurnEntities(t)
	if _, err := s.OpenTurnContext("t1", "demo", "", "tg:1", "u1", "", nil); err != nil {
		t.Fatal(err)
	}
	if err := s.SetTurnState("t1", TurnDone); err != nil {
		t.Fatal(err)
	}
	if _, ok := liveTurn(t, s, "t1"); !ok {
		t.Fatal("a done turn left turn_context before its run returned")
	}
	if err := s.SetRunReturned("t1"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetTurnState("t1", TurnDone); err != nil {
		t.Fatal(err)
	}
	if _, ok := liveTurn(t, s, "t1"); ok {
		t.Fatal("a done turn whose run returned is still in turn_context")
	}
}

// A turn whose status is still owed stays live until the acknowledgement that
// clears it, which moves it.
func TestOwedStatusKeepsTheTurnLiveUntilAcknowledged(t *testing.T) {
	s := openTurnEntities(t)
	if _, err := s.OpenTurnContext("t1", "demo", "", "tg:1", "u1", "", nil); err != nil {
		t.Fatal(err)
	}
	if err := s.SetRunReturned("t1"); err != nil {
		t.Fatal(err)
	}
	tc, _ := s.TurnContextOf("t1")
	if ok, err := s.FinishTurnStatus(t.Context(), tc); err != nil || !ok {
		t.Fatalf("finish status: %v %v", ok, err)
	}
	if _, ok := liveTurn(t, s, "t1"); !ok {
		t.Fatal("a turn that owes its status left turn_context")
	}
	owed, err := s.OwedTurnStatuses(t.Context())
	if err != nil || len(owed) != 1 {
		t.Fatalf("owed = %v err %v", owed, err)
	}
	if err := s.AcknowledgeTurnStatus(t.Context(), owed[0]); err != nil {
		t.Fatal(err)
	}
	if _, ok := liveTurn(t, s, "t1"); ok {
		t.Fatal("the acknowledged turn is still in turn_context")
	}
	if it, ok := archivedTurn(t, s, "t1"); !ok || it.Attrs[attrTurnState] != TurnDone {
		t.Fatalf("archive: %v ok=%v", it.Attrs, ok)
	}
}

// The expiry sweep finishes a turn too.
func TestExpiredTurnMovesToTheArchive(t *testing.T) {
	s := openTurnEntities(t)
	if err := s.SeedItem(t.Context(), TurnContexts, []string{"stale"}, map[string]string{
		attrTurnFolder: "demo", attrTurnState: TurnRunning, "attempt_id": "a",
		AttrTurnStartedAt: "2026-01-01T00:00:00.000000000Z",
	}); err != nil {
		t.Fatal(err)
	}
	if n, err := s.SweepExpiredRunning(0); err != nil || n != 1 {
		t.Fatalf("swept %d err %v", n, err)
	}
	if _, ok := liveTurn(t, s, "stale"); ok {
		t.Fatal("the expired turn is still in turn_context")
	}
	if it, ok := archivedTurn(t, s, "stale"); !ok || it.Attrs[attrTurnState] != TurnExpired {
		t.Fatalf("archive: %v ok=%v", it.Attrs, ok)
	}
}

// An expired turn may be bound again, afresh; a done one may not.
func TestAnArchivedExpiredTurnBindsAgain(t *testing.T) {
	s := openTurnEntities(t)
	if err := s.SeedItem(t.Context(), TurnArchive, []string{"stale"}, map[string]string{
		attrTurnFolder: "demo", attrTurnState: TurnExpired,
	}); err != nil {
		t.Fatal(err)
	}
	live, err := s.OpenTurnContext("stale", "demo", "", "tg:1", "u1", "", nil)
	if err != nil || !live {
		t.Fatalf("rebind of an expired turn: live=%v err=%v", live, err)
	}
	if tc, ok := s.TurnContextOf("stale"); !ok || tc.State != TurnRunning {
		t.Fatalf("by id: %+v", tc)
	}
}

type turnWriteRace struct {
	objstore.Store
	once sync.Once
	s    *Store
}

func (r *turnWriteRace) GuardedBatch(ctx context.Context, checks []objstore.Check,
	writes []objstore.Write) (bool, error) {
	for _, w := range writes {
		if w.Entity == TurnArchive {
			r.once.Do(func() {
				_ = r.s.AddTurnStatusChat(ctx, w.Key.Sort, "tg:late")
			})
		}
	}
	return r.Store.GuardedBatch(ctx, checks, writes)
}

// An attribute written between the read and the move is carried, not lost: the
// move requires the item unchanged and reads it again.
func TestArchiveMoveKeepsAConcurrentWrite(t *testing.T) {
	s := openTurnEntities(t)
	if _, err := s.OpenTurnContext("t1", "demo", "", "tg:1", "u1", "", nil); err != nil {
		t.Fatal(err)
	}
	if err := s.SetRunReturned("t1"); err != nil {
		t.Fatal(err)
	}
	racy := NewOver(&turnWriteRace{Store: s.Obj(), s: s})
	if err := racy.SetTurnState("t1", TurnDone); err != nil {
		t.Fatal(err)
	}
	it, ok := archivedTurn(t, s, "t1")
	if !ok || it.Attrs["status_chats"] != `["tg:late"]` {
		t.Fatalf("archive: %v ok=%v", it.Attrs, ok)
	}
}

// The startup migration moves every finished row once, whole, leaves the live
// ones, records their runs, and does nothing on a later start.
func TestMigrateTurnArchiveMovesFinishedRowsOnce(t *testing.T) {
	s := openTurnEntities(t)
	ctx := t.Context()
	rows := map[string]map[string]string{
		"done":    {attrTurnFolder: "demo", attrTurnState: TurnDone, attrTurnRunID: "r0", "nudged": "1"},
		"expired": {attrTurnFolder: "demo", attrTurnState: TurnExpired},
		"owed":    {attrTurnFolder: "demo", attrTurnState: TurnDone, "status_owed": "final"},
		"running": {attrTurnFolder: "demo", attrTurnState: TurnRunning, attrTurnRunID: "r1", attrTurnChatJID: "tg:1"},
	}
	for id, attrs := range rows {
		if err := s.SeedItem(ctx, TurnContexts, []string{id}, attrs); err != nil {
			t.Fatal(err)
		}
	}
	for i := range 2 * archiveBatch {
		if err := s.SeedItem(ctx, TurnContexts, []string{core.MsgID("bulk")},
			map[string]string{attrTurnState: TurnDone, "n": string(rune('a' + i%26))}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.MigrateTurnArchive(ctx); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"done", "expired"} {
		if _, ok := liveTurn(t, s, id); ok {
			t.Errorf("%s is still live", id)
		}
		if it, ok := archivedTurn(t, s, id); !ok || !maps.Equal(it.Attrs, rows[id]) {
			t.Errorf("%s archived as %v, want %v", id, it.Attrs, rows[id])
		}
	}
	for _, id := range []string{"owed", "running"} {
		if _, ok := liveTurn(t, s, id); !ok {
			t.Errorf("%s left turn_context", id)
		}
	}
	live, err := s.Obj().Query(ctx, objstore.Query{Entity: TurnContexts, Partition: OwnerRoutd})
	if err != nil || len(live) != 2 {
		t.Fatalf("turn_context holds %d rows, want the 2 live: %v", len(live), err)
	}
	archived, err := s.Obj().Query(ctx, objstore.Query{Entity: TurnArchive, Partition: OwnerRoutd})
	if err != nil || len(archived) != 2+2*archiveBatch {
		t.Fatalf("turn_archive holds %d rows, want %d: %v", len(archived), 2+2*archiveBatch, err)
	}
	if got := s.TypingJIDsForRun("r1"); len(got) != 1 || got[0] != "tg:1" {
		t.Fatalf("run of the live turn: %v", got)
	}
	if err := s.SeedItem(ctx, TurnContexts, []string{"later"},
		map[string]string{attrTurnState: TurnDone}); err != nil {
		t.Fatal(err)
	}
	if err := s.MigrateTurnArchive(ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok := liveTurn(t, s, "later"); !ok {
		t.Fatal("the migration ran a second time")
	}
}
