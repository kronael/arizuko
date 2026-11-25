package audit

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/objstore/dynstore"
)

// recordAt writes one event at an instant, the way the import writes a row
// whose time is not now.
func recordAt(t *testing.T, s *dynstore.Store, at time.Time, action string) {
	t.Helper()
	w := writeFor(NewID(at), Event{Category: CategorySystem, Action: action, Actor: "system"}, "")
	if ok, err := s.GuardedBatch(t.Context(), nil, []objstore.Write{w}); err != nil || !ok {
		t.Fatalf("record %s: %v %v", action, ok, err)
	}
}

func actionsOf(rows []Row) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.Action
	}
	return out
}

// Prune deletes every event older than the bound, across more than one batch,
// and keeps the event AT the bound and everything newer.
func TestPruneDeletesWhatIsOlderThanTheBound(t *testing.T) {
	s := openTestDB(t)
	bound := time.Date(2026, 8, 27, 0, 0, 0, 0, time.UTC)
	old := 2*objstore.MaxBatchActions + 7
	for i := range old {
		recordAt(t, s, bound.Add(-time.Duration(i+1)*time.Minute), fmt.Sprint("old-", i))
	}
	recordAt(t, s, bound, "at-the-bound")
	recordAt(t, s, bound.Add(time.Hour), "newer")

	n, err := Prune(t.Context(), s, bound)
	if err != nil {
		t.Fatal(err)
	}
	if n != old {
		t.Errorf("Prune deleted %d events, want the %d older than the bound", n, old)
	}
	if got := fmt.Sprint(actionsOf(rowsOf(t, s))); got != "[newer at-the-bound]" {
		t.Errorf("the log holds %s after the prune, want [newer at-the-bound]", got)
	}
	if n, err := Prune(t.Context(), s, bound); err != nil || n != 0 {
		t.Errorf("a second prune deleted %d (%v), want 0", n, err)
	}
}

// The loop prunes to Retention: an event 91 days old goes, one 89 days old
// stays, and the loop ends with its context.
func TestPruneLoopKeepsNinetyDays(t *testing.T) {
	if Retention != 90*24*time.Hour {
		t.Fatalf("Retention = %v, want 90 days", Retention)
	}
	s := openTestDB(t)
	now := time.Now()
	recordAt(t, s, now.Add(-91*24*time.Hour), "expired")
	recordAt(t, s, now.Add(-89*24*time.Hour), "kept")

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		PruneLoop(ctx, s)
	}()
	deadline := time.Now().Add(20 * time.Second)
	for len(rowsOf(t, s)) != 1 {
		if time.Now().After(deadline) {
			t.Fatalf("the loop left %v", actionsOf(rowsOf(t, s)))
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got := rowsOf(t, s)[0]; got.Action != "kept" ||
		got.CreatedAt != core.TS(now.Add(-89*24*time.Hour)) {
		t.Errorf("the loop kept %+v, want the event of 89 days ago", got)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("PruneLoop did not end with its context")
	}
}
