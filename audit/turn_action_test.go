package audit

import (
	"context"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
)

// Delivery reads the exact successful turn action, beyond the history page's
// cap. Another turn, folder, action, or failed operation must not count.
func TestHasSuccessfulTurnAction(t *testing.T) {
	s := openTestDB(t)
	ctx := context.Background()
	since := time.Now()
	seed(t, s, Event{Category: CategoryAgent, Action: "message.like", Actor: "folder:demo", Folder: "demo", TurnID: "answered", Outcome: OutcomeOK})
	for range MaxLimit + 1 {
		seed(t, s, Event{Category: CategoryAgent, Action: "message.like", Actor: "folder:demo", Folder: "demo", TurnID: "other", Outcome: OutcomeError})
	}
	for _, tt := range []struct {
		turn, folder, action string
		want                 bool
	}{
		{"answered", "demo", "message.like", true},
		{"other", "demo", "message.like", false},
		{"answered", "foreign", "message.like", false},
		{"answered", "demo", "message.delete", false},
	} {
		got, err := HasSuccessfulTurnAction(ctx, s, tt.turn, tt.folder, since, tt.action)
		if err != nil || got != tt.want {
			t.Fatalf("%+v got=%v err=%v", tt, got, err)
		}
	}
}

type turnActionReads struct {
	objstore.Store
	queries []objstore.Query
}

func (s *turnActionReads) Query(_ context.Context, q objstore.Query) ([]objstore.Item, error) {
	s.queries = append(s.queries, q)
	return []objstore.Item{{Attrs: map[string]string{attrAction: "message.like"}}}, nil
}

// Dynamo filters after its page limit. A key lower bound and one uncapped query
// avoid six full-history walks; the SQL surrogate alone cannot prove this cost.
func TestTurnActionReadIsBoundedByStartedAt(t *testing.T) {
	s := &turnActionReads{}
	ctx := context.Background()
	if _, err := HasSuccessfulTurnAction(ctx, s, "t1", "demo", time.Time{}, "message.like"); err == nil || len(s.queries) != 0 {
		t.Fatal("missing started_at must not read the whole audit history")
	}
	since := time.Date(2026, 10, 19, 16, 0, 0, 0, time.UTC)
	ok, err := HasSuccessfulTurnAction(ctx, s, "t1", "demo", since, "message.like", "message.delete")
	if err != nil || !ok || len(s.queries) != 1 {
		t.Fatalf("ok=%v err=%v queries=%d", ok, err, len(s.queries))
	}
	q := s.queries[0]
	if q.Key.Op != objstore.GeKey || q.Key.Value != idAt(core.TS(since), "00000000") || q.Limit != 0 {
		t.Fatalf("unbounded or one-row-page query: %+v", q)
	}
	for attr, value := range map[string]string{attrTurnID: "t1", attrFolder: "demo", attrCategory: CategoryAgent, attrOutcome: OutcomeOK} {
		found := false
		for _, cmp := range q.Where {
			if cmp.Attr == attr && cmp.Op == objstore.Eq && cmp.Value == value {
				found = true
			}
		}
		if !found {
			t.Fatalf("query lost %s=%q: %+v", attr, value, q)
		}
	}
}
