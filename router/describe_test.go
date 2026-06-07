package router

import (
	"testing"

	"github.com/kronael/arizuko/core"
)

func find(views []RouteView, id int64) *RouteView {
	for i := range views {
		if views[i].ID == id {
			return &views[i]
		}
	}
	return nil
}

// TestDescribe_ModeAndTrigger covers the three intents an agent must tell
// apart: bare trigger (fires on everything), mention trigger, and observe.
func TestDescribe_ModeAndTrigger(t *testing.T) {
	views := Describe([]core.Route{
		{ID: 1, Seq: 0, Match: "chat_jid=slack:w/channel/A verb=mention", Target: "atlas/support"},
		{ID: 2, Seq: 1, Match: "chat_jid=slack:w/channel/A", Target: "atlas/support#observe"},
		{ID: 3, Seq: 2, Match: "chat_jid=slack:w/channel/B", Target: "atlas/general"},
	})

	mention := find(views, 1)
	if mention.Mode != "trigger" || !mention.FiresTurn || mention.TriggersOn != "verb=mention" {
		t.Fatalf("mention row: %+v", mention)
	}
	observe := find(views, 2)
	if observe.Mode != "observe" || observe.FiresTurn || observe.TriggersOn != "" {
		t.Fatalf("observe row: %+v", observe)
	}
	bare := find(views, 3)
	if bare.Mode != "trigger" || !bare.FiresTurn || bare.TriggersOn != "every message" {
		t.Fatalf("bare trigger row should fire on every message: %+v", bare)
	}
}

// TestDescribe_ShadowDeadRows reproduces marinade's #search bug: a bare
// trigger at seq 0 (matched by room=) swallows the intended mention trigger
// and observe catch-all below it, so both are dead. Note room= and chat_jid=
// address the same chat — shadow detection must see through that.
func TestDescribe_ShadowDeadRows(t *testing.T) {
	views := Describe([]core.Route{
		{ID: 29, Seq: 0, Match: "room=w/channel/S", Target: "atlas/search"},
		{ID: 30, Seq: 9, Match: "chat_jid=slack:w/channel/S verb=mention", Target: "atlas/search"},
		{ID: 31, Seq: 100, Match: "chat_jid=slack:w/channel/S", Target: "atlas/search#observe"},
	})

	if v := find(views, 29); v.ShadowedBy != 0 {
		t.Fatalf("seq-0 rule cannot be shadowed: %+v", v)
	}
	if v := find(views, 30); v.ShadowedBy != 29 {
		t.Fatalf("mention rule should be shadowed by 29: %+v", v)
	}
	if v := find(views, 31); v.ShadowedBy != 29 {
		t.Fatalf("observe rule should be shadowed by 29: %+v", v)
	}
}

// TestDescribe_SpecificDoesNotShadowCatchall reproduces marinade's #general
// shape: a specific bare trigger sits above a broad observe catch-all. The
// catch-all is still alive for every OTHER channel, so it must not be flagged.
func TestDescribe_SpecificDoesNotShadowCatchall(t *testing.T) {
	views := Describe([]core.Route{
		{ID: 26, Seq: 9, Match: "chat_jid=slack:w/channel/G", Target: "atlas/general"},
		{ID: 20, Seq: 40, Match: "chat_jid=slack:*/channel/* verb=mention", Target: "atlas"},
		{ID: 14, Seq: 100, Match: "chat_jid=slack:*/channel/*", Target: "atlas#observe"},
	})

	if v := find(views, 14); v.ShadowedBy != 0 {
		t.Fatalf("broad observe catch-all is live for other channels, not shadowed: %+v", v)
	}
	if v := find(views, 20); v.ShadowedBy != 0 {
		t.Fatalf("broad mention catch-all is live for other channels, not shadowed: %+v", v)
	}
}
