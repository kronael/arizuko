package store

import (
	"context"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
)

// The read names the observer alone, so it is a begins_with over a leading
// fragment of the key. Another observer's subscriptions share the partition and
// must not arrive in it.
func TestWatchedSourcesAnswersOneObserversSubscriptions(t *testing.T) {
	s := openEntities(t)
	for _, pair := range [][2]string{{"root", "child"}, {"root", "other"}, {"stranger", "child"}} {
		if err := s.AddGroupWatcher(pair[0], pair[1]); err != nil {
			t.Fatal(err)
		}
	}
	got := s.WatchedSources("root")
	if len(got) != 2 {
		t.Fatalf("root watches %v, want two: the read reached another observer's rows or "+
			"missed its own", got)
	}
	if got[0] != "child" || got[1] != "other" {
		t.Errorf("root watches %v, want [child other] in key order", got)
	}
	if only := s.WatchedSources("stranger"); len(only) != 1 || only[0] != "child" {
		t.Errorf("stranger watches %v, want [child]", only)
	}
}

// Subscribing is IDEMPOTENT and not a claim: no caller reads whether the write
// was the first, and the agent's observe_group tool asks for the subscription to
// exist. A refused create is that state, so it is success.
func TestSubscribingTwiceIsNotAnError(t *testing.T) {
	s := openEntities(t)
	if err := s.AddGroupWatcher("root", "child"); err != nil {
		t.Fatal(err)
	}
	if err := s.AddGroupWatcher("root", "child"); err != nil {
		t.Fatalf("a repeated subscription errored: %v", err)
	}
	if got := s.WatchedSources("root"); len(got) != 1 {
		t.Fatalf("root watches %v, want one item", got)
	}
	// Cancelling one that is not there is the same shape from the other side.
	if err := s.RemoveGroupWatcher("root", "never"); err != nil {
		t.Fatalf("cancelling an absent subscription errored: %v", err)
	}
}

// A subscription carries TWO folder coordinates, so the group delete purges by
// EITHER end: leaving the observer's row hands the next tenant of that path
// another folder's inbound messages, and leaving the source's row hands its
// messages to whoever takes the observer's path.
//
// Drop either entityScopePurge entry for GroupWatchers and one half of this
// goes red.
func TestAGroupDeleteTakesTheSubscriptionsAtBothEnds(t *testing.T) {
	s := openEntities(t)
	for _, f := range []string{"doomed", "keeper", "doomed/kid"} {
		if err := s.PutGroupRow(core.Group{Folder: f, AddedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	subs := [][2]string{
		{"doomed", "keeper"},     // the deleted folder observes
		{"keeper", "doomed"},     // the deleted folder is observed
		{"doomed/kid", "keeper"}, // a child of the deleted folder observes
		{"keeper", "keeper"},     // neither end is inside the subtree
	}
	for _, pair := range subs {
		if err := s.AddGroupWatcher(pair[0], pair[1]); err != nil {
			t.Fatal(err)
		}
	}

	if _, _, err := s.DeleteGroup("doomed"); err != nil {
		t.Fatalf("DeleteGroup: %v", err)
	}

	if got := s.WatchedSources("doomed"); len(got) != 0 {
		t.Errorf("the deleted folder still watches %v; the next tenant of that path reads "+
			"another folder's messages as ambient context", got)
	}
	if got := s.WatchedSources("doomed/kid"); len(got) != 0 {
		t.Errorf("a child of the deleted folder still watches %v", got)
	}
	watched := s.WatchedSources("keeper")
	if len(watched) != 1 || watched[0] != "keeper" {
		t.Errorf("keeper watches %v, want [keeper]: the subscription naming the deleted "+
			"folder as its SOURCE survived, so its messages reach whoever takes that path — "+
			"or the surviving pair went with it", watched)
	}
	if n, err := s.CountGroupWatchers(context.Background()); err != nil || n != 1 {
		t.Fatalf("%d subscriptions left (err=%v), want the one naming neither end", n, err)
	}
}

// The coordinate joins two folders on one byte, so a folder carrying that byte
// would split the key somewhere the read never puts it back together.
func TestAWatcherCoordinateCarryingTheSeparatorIsRefused(t *testing.T) {
	s := openEntities(t)
	if err := s.AddGroupWatcher("root"+KeySep+"x", "child"); err == nil {
		t.Error("an observer carrying the coordinate separator was accepted")
	}
	if err := s.AddGroupWatcher("root", "child"+KeySep+"y"); err == nil {
		t.Error("a source carrying the coordinate separator was accepted")
	}
	if n, err := s.CountGroupWatchers(context.Background()); err != nil || n != 0 {
		t.Fatalf("%d items landed (err=%v); a refused coordinate wrote one anyway", n, err)
	}
}
