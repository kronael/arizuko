package routd

import (
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/store"
)

// An operator extends engagement to CI with one `#engaged` row above the seeded
// `#observe` fallback (ROUTING.md §"A target fragment sets the mode",
// specs/6/27-git-channel.md). The row fires while the chat and topic are
// engaged and is observed otherwise; it is never addressed.
func engagedCIRoutes(t *testing.T) []core.Route {
	t.Helper()
	routes, err := store.GitHubSourceRoutes("repo/kronael/arizuko", "eng")
	if err != nil {
		t.Fatal(err)
	}
	return append([]core.Route{{Seq: 50, Match: "chat_jid=github:kronael/arizuko/pr/* verb=check_run", Target: "eng#engaged"}}, routes...)
}

func TestEngagedRoute_ACIRowOnAnEngagedPRFiresAndIsNotAddressed(t *testing.T) {
	db, loop := newEngagementLoop(t, engagedCIRoutes(t))
	jid := "github:kronael/arizuko/pr/5"
	if err := db.SetEngagement(jid, "pr/5", "held", time.Hour); err != nil {
		t.Fatal(err)
	}
	got := loop.resolve(jid, core.Message{ChatJID: jid, Topic: "pr/5", Verb: "check_run"})
	if !got.ok || got.Folder != "held" || got.Addressed || got.Observe != "" {
		t.Fatalf("check_run on an engaged PR: %+v want folder=held, not addressed", got)
	}
	// The default still holds: a review row has no #engaged row and is observed.
	got = loop.resolve(jid, core.Message{ChatJID: jid, Topic: "pr/5", Verb: "pull_request_review"})
	if got.ok || got.Observe != "eng" {
		t.Fatalf("pull_request_review on an engaged PR: %+v want observe=eng", got)
	}
}

func TestEngagedRoute_TheSameRowWithoutEngagementIsStoredObserved(t *testing.T) {
	_, loop := newEngagementLoop(t, engagedCIRoutes(t))
	jid := "github:kronael/arizuko/pr/6"
	got := loop.resolve(jid, core.Message{ChatJID: jid, Topic: "pr/6", Verb: "check_run"})
	if got.ok || got.Observe != "eng" {
		t.Fatalf("check_run on an unengaged PR: %+v want observe=eng", got)
	}
}

func TestEngagedRoute_ARootWindowCoversThePRTopic(t *testing.T) {
	db, loop := newEngagementLoop(t, engagedCIRoutes(t))
	jid := "github:kronael/arizuko/pr/7"
	if err := db.SetEngagement(jid, "", "held", time.Hour); err != nil {
		t.Fatal(err)
	}
	// The root-topic window covers the PR topic until the thread disengages.
	got := loop.resolve(jid, core.Message{ChatJID: jid, Topic: "pr/7", Verb: "check_run"})
	if !got.ok || got.Folder != "held" {
		t.Fatalf("root window must cover the topic: %+v", got)
	}
}

// A mention and a newer CI row in one batch: the CI row dispatches through its
// #engaged rule, and only the mention is addressed (the CI row is never).
func TestEngagedRoute_AMixedBatchAddressesOnlyTheMentionAndBothRowsDispatch(t *testing.T) {
	db, loop := newEngagementLoop(t, engagedCIRoutes(t))
	jid := "github:kronael/arizuko/pr/8"
	if err := db.SetEngagement(jid, "pr/8", "eng", time.Hour); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	b := loop.resolveBatch(jid, []core.Message{
		{ID: "gh-rc1", ChatJID: jid, Topic: "pr/8", Sender: "github:user/alice", Verb: "mention", Timestamp: now},
		{ID: "gh-ci1", ChatJID: jid, Topic: "pr/8", Sender: "github:user/ci", Verb: "check_run", Timestamp: now.Add(time.Millisecond)},
	})
	if !b.ok || b.Folder != "eng" {
		t.Fatalf("batch: %+v want folder=eng", b.resolution)
	}
	if !b.rows[0].ok || !b.rows[1].ok || !b.rows[0].Addressed || b.rows[1].Addressed {
		t.Fatalf("rows=%+v want both dispatching, only the mention addressed", b.rows)
	}
}

// Without engagement the CI row is only context: the mention alone dispatches.
func TestEngagedRoute_AMixedBatchWithoutEngagementObservesTheCIRow(t *testing.T) {
	_, loop := newEngagementLoop(t, engagedCIRoutes(t))
	jid := "github:kronael/arizuko/pr/9"
	now := time.Now().UTC()
	b := loop.resolveBatch(jid, []core.Message{
		{ID: "gh-rc1", ChatJID: jid, Topic: "pr/9", Sender: "github:user/alice", Verb: "mention", Timestamp: now},
		{ID: "gh-ci1", ChatJID: jid, Topic: "pr/9", Sender: "github:user/ci", Verb: "check_run", Timestamp: now.Add(time.Millisecond)},
	})
	if !b.ok || b.Folder != "eng" || !b.Addressed {
		t.Fatalf("batch: %+v want folder=eng addressed", b.resolution)
	}
	if !b.rows[0].ok || b.rows[1].ok {
		t.Fatalf("rows=%+v want only the mention", b.rows)
	}
}

// Through the worker: the CI row alone starts a turn on an engaged PR, and on an
// unengaged one it is stored observed with no turn.
func TestEngagedRoute_TheWorkerFiresOnlyWhileEngaged(t *testing.T) {
	for _, c := range []struct {
		name    string
		engaged bool
		turns   int
	}{{"engaged", true, 1}, {"unengaged", false, 0}} {
		t.Run(c.name, func(t *testing.T) {
			db, err := OpenMem()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { db.Close() })
			if err := db.PutGroup(core.Group{Folder: "eng"}); err != nil {
				t.Fatal(err)
			}
			doSetRoutes(t, db, engagedCIRoutes(t))
			rr := &recRunner{}
			loop := NewLoop(db, rr, LoopConfig{ObserveWindowMessages: 20, ObserveWindowChars: 1000})
			t.Cleanup(loop.StopQueue)
			if c.engaged {
				if err := db.SetEngagement(prJID, "pr/311", "eng", time.Hour); err != nil {
					t.Fatal(err)
				}
			}
			putPRRow(t, db, "gh-ci1", "github:user/ci", "check_run", "tests failed", time.Now().UTC())

			if _, err := loop.processGroupMessages(prJID); err != nil {
				t.Fatalf("process: %v", err)
			}
			if len(rr.runs) != c.turns {
				t.Fatalf("turns=%d want %d: %+v", len(rr.runs), c.turns, rr.runs)
			}
			// routed_to marks an observed row; a row that fires a turn has none.
			if got := msgByID(t, db, prJID, "gh-ci1").RoutedTo; (got == "eng") == c.engaged {
				t.Fatalf("CI row routed_to=%q with engaged=%v", got, c.engaged)
			}
		})
	}
}

// Addressed is a fact about each turn, not the batch: on an engaged PR a
// mention and another sender's CI row run as two turns, and only the mention's
// turn is owed an answer.
func TestEngagedRoute_TheCITurnOfAMixedBatchIsNotAddressed(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.PutGroup(core.Group{Folder: "eng"}); err != nil {
		t.Fatal(err)
	}
	doSetRoutes(t, db, engagedCIRoutes(t))
	rr := &recRunner{}
	loop := NewLoop(db, rr, LoopConfig{ObserveWindowMessages: 20, ObserveWindowChars: 1000})
	t.Cleanup(loop.StopQueue)
	if err := db.SetEngagement(prJID, "pr/311", "eng", time.Hour); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	putPRRow(t, db, "gh-rc1", "github:user/alice", "mention", "inline mention", now)
	putPRRow(t, db, "gh-ci1", "github:user/ci", "check_run", "tests failed", now.Add(time.Millisecond))

	if _, err := loop.processGroupMessages(prJID); err != nil {
		t.Fatalf("process: %v", err)
	}
	if len(rr.runs) != 2 {
		t.Fatalf("turns=%d want 2 (one per sender): %+v", len(rr.runs), rr.runs)
	}
	for id, want := range map[string]bool{"gh-rc1": true, "gh-ci1": false} {
		tc, ok := db.GetTurnContext(id)
		if !ok || tc.Addressed != want {
			t.Errorf("turn %s: bound=%v addressed=%v, want addressed=%v", id, ok, tc.Addressed, want)
		}
	}
}
