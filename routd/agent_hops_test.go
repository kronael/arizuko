package routd

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	apiv1 "github.com/kronael/arizuko/routd/api/v1"
	runedv1 "github.com/kronael/arizuko/runed/api/v1"
	"github.com/kronael/arizuko/store"
)

// TestAgentHopsBound pins BUGS L807: live sloth and sloth/trading started turns
// in each other for 80 minutes because no row recorded its distance from a
// person. A person asks a, a sends to b; b can answer but cannot start a turn
// in a (the loop) or in c (a chain). A scheduled task's input is 0 hops, and a
// replayed turn counts the hops of its saved input.
func TestAgentHopsBound(t *testing.T) {
	db, srv, _ := newTestRoutd(t)
	for _, folder := range []string{"a", "b", "c"} {
		if err := db.PutGroup(core.Group{Folder: folder}); err != nil {
			t.Fatal(err)
		}
	}
	doSetRoutes(t, db, []core.Route{{Match: "chat_jid=telegram:group/1", Target: "a"}})
	plan := map[string][]string{"a": {"b"}, "b": {"a", "c"}}
	sent := map[string]error{}
	srv.loop.runner = runnerFn(func(_ context.Context, req runedv1.RunRequest) (runedv1.RunOutcome, error) {
		folder := string(req.Folder)
		put := srv.buildStoreFns(turnMCP{folder: folder, turnID: req.TurnID}).PutMessage
		for _, target := range plan[folder] {
			_, err := store.PostGroup(put, target, "work for "+target, folder)
			sent[folder+">"+target] = err
		}
		return runedv1.RunOutcome{RunID: "r", Outcome: runedv1.OutcomeOK}, nil
	})

	if err := db.PutMessage(core.Message{ID: "human", ChatJID: "telegram:group/1", Sender: "telegram:user/2",
		Content: "ask b", Timestamp: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.loop.processGroupMessages("telegram:group/1"); err != nil {
		t.Fatal(err)
	}
	if err := sent["a>b"]; err != nil {
		t.Fatalf("person -> a -> b refused: %v", err)
	}
	inB, _ := db.MessagesSince("b", "")
	if len(inB) != 1 || inB[0].Hops != 1 {
		t.Fatalf("b input = %+v, want one row at hop 1", inB)
	}
	if _, err := srv.loop.processGroupMessages("b"); err != nil {
		t.Fatal(err)
	}
	for _, edge := range []string{"b>a", "b>c"} {
		if err := sent[edge]; err == nil || !strings.Contains(err.Error(), "the limit is 2") {
			t.Fatalf("%s = %v, want refused at the hop limit", edge, err)
		}
	}
	for _, folder := range []string{"a", "c"} {
		msgs, _ := db.MessagesSince(folder, "")
		for _, m := range msgs {
			if !m.BotMsg {
				t.Fatalf("refused send still landed in %s: %+v", folder, m)
			}
		}
	}

	// The send reads the turn's saved input, the same rows a retry or a restart
	// replays, so the hop count survives both.
	keys, err := turnMessageKeys(inB)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := db.store().DeferredMessages(context.Background(), keys)
	if err != nil || len(saved) != 1 || saved[0].Hops != 1 {
		t.Fatalf("saved input = %+v %v, want hop 1", saved, err)
	}

	// A scheduled task's prompt starts at 0 hops: its turn may send once.
	if _, err := srv.ingestMessage(apiv1.Message{ID: "sched-1", ChatJID: "a", Sender: core.ScheduledSender,
		Content: "daily", Timestamp: time.Now().Unix()}, "service:routd", ""); err != nil {
		t.Fatal(err)
	}
	delete(sent, "a>b")
	if _, err := srv.loop.processGroupMessages("a"); err != nil {
		t.Fatal(err)
	}
	if err, ok := sent["a>b"]; !ok || err != nil {
		t.Fatalf("scheduled a -> b = %v (ran %v), want allowed", err, ok)
	}
}
