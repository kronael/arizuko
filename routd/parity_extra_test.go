package routd

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/store"
)

// Tier-2 parity: observeWindow override + the startup re-feed +
// LatestSource resolution, all shipped but under-tested vs gated.

// --- observeWindow first-route-wins (mirror gateway_extra) ---

// TestObserveWindow_FirstRouteWins: with two routes targeting the same folder
// carrying different observe-window overrides, the first inserted wins (gated
// TestObserveWindow_FirstRouteWins).
func TestObserveWindow_FirstRouteWins(t *testing.T) {
	db, l := targetLoop(t)
	_ = db.PutGroup(core.Group{Folder: "grp", AddedAt: time.Now().UTC()})
	if _, err := db.AddRoute(core.Route{Seq: 0, Match: "room=a", Target: "grp",
		ObserveWindowMessages: 5, ObserveWindowChars: 100}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.AddRoute(core.Route{Seq: 1, Match: "room=b", Target: "grp",
		ObserveWindowMessages: 9, ObserveWindowChars: 900}); err != nil {
		t.Fatal(err)
	}
	n, c := l.observeWindow("grp")
	if n != 5 || c != 100 {
		t.Errorf("observeWindow = (%d, %d), want (5, 100) from the first matching route", n, c)
	}
}

// TestObserveWindow_GroupWindow: the window is the config default, then the
// group's stored window replaces it field by field, then the first route
// override replaces that.
func TestObserveWindow_GroupWindow(t *testing.T) {
	cases := []struct {
		name         string
		groupMsgs    int
		groupChars   int
		route        *core.Route
		wantN, wantC int
	}{
		{"no group window, no route", -1, -1, nil, 20, 1000},
		{"group window replaces the default", 3, 100, nil, 3, 100},
		{"group messages only keep the default chars", 3, -1, nil, 3, 1000},
		{"group chars only keep the default messages", -1, 100, nil, 20, 100},
		{"route override replaces the group window", 3, 100,
			&core.Route{ObserveWindowMessages: 7, ObserveWindowChars: 700}, 7, 700},
		{"route messages only keep the group chars", 3, 100,
			&core.Route{ObserveWindowMessages: 7}, 7, 100},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			db, l := targetLoop(t)
			l.observeMessages, l.observeChars = 20, 1000
			_ = db.PutGroup(core.Group{Folder: "grp", AddedAt: time.Now().UTC()})
			if err := db.SetGroupObserveWindow("grp", c.groupMsgs, c.groupChars); err != nil {
				t.Fatalf("set the group window: %v", err)
			}
			if c.route != nil {
				r := *c.route
				r.Match, r.Target = "room=a", "grp"
				if _, err := db.AddRoute(r); err != nil {
					t.Fatalf("add the route: %v", err)
				}
			}
			n, chars := l.observeWindow("grp")
			if n != c.wantN || chars != c.wantC {
				t.Errorf("observeWindow = (%d, %d), want (%d, %d)", n, chars, c.wantN, c.wantC)
			}
		})
	}
}

// --- the startup re-feed (mirror gateway_test:TestRecoverPendingMessages) ---

// TestStartup_ReFeedsRunningTurns: on boot, the startup sweep re-feeds every
// chat whose running turn left its input past the un-advanced cursor, so a
// crash mid-turn re-attempts it (turn_results dedups the old run's
// submit_turn); a terminal turn's chat, its cursor past the input, is not.
func TestStartup_ReFeedsRunningTurns(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	l := NewLoop(db, &recRunner{}, LoopConfig{})
	// queue stays live so EnqueueMessageCheck flows to processGroupMessages.

	recovered := map[string]bool{}
	var mu sync.Mutex
	l.q.SetProcessMessagesFn(func(jid string) (bool, error) {
		mu.Lock()
		recovered[jid] = true
		mu.Unlock()
		return true, nil
	})

	if err := db.store().PutGroup(core.Group{Folder: "demo"}); err != nil {
		t.Fatal(err)
	}
	// Two chats with running turns; one with a terminal turn (must NOT re-feed).
	if _, err := db.PutTurnContext("t1", "demo", "", "tg:100", "u", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := db.PutTurnContext("t2", "demo", "", "discord:200", "u", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := db.PutTurnContext("t3", "demo", "", "tg:300", "u", ""); err != nil {
		t.Fatal(err)
	}
	// t3 reached a terminal state and its cursor passed the input.
	if _, err := db.RecordTurnResult("demo", "t3", "s", "success", "", store.TurnUsage{}); err != nil {
		t.Fatal(err)
	}
	if err := db.SetTurnState("t3", "done"); err != nil {
		t.Fatal(err)
	}
	for i, jid := range []string{"tg:100", "discord:200", "tg:300"} {
		in := core.Message{ID: fmt.Sprintf("t%d", i+1), ChatJID: jid, Sender: "u",
			Content: "q", Timestamp: time.Now().UTC()}
		if err := db.PutMessage(in); err != nil {
			t.Fatal(err)
		}
		if jid == "tg:300" {
			if err := db.SetAgentCursor(jid, store.MessageCursor(in)); err != nil {
				t.Fatal(err)
			}
		}
	}

	l.recoverPending()
	l.sweepChats(t.Context())

	want := []string{"tg:100", "discord:200"}
	deadline := time.After(2 * time.Second)
	for {
		mu.Lock()
		done := true
		for _, jid := range want {
			if !recovered[jid] {
				done = false
				break
			}
		}
		mu.Unlock()
		if done {
			break
		}
		select {
		case <-deadline:
			mu.Lock()
			defer mu.Unlock()
			for _, jid := range want {
				if !recovered[jid] {
					t.Errorf("%s not recovered", jid)
				}
			}
			return
		case <-time.After(10 * time.Millisecond):
		}
	}
	// the terminal-turn chat must not have been re-fed.
	mu.Lock()
	defer mu.Unlock()
	if recovered["tg:300"] {
		t.Error("startup re-fed a chat whose turn was already terminal")
	}
}

// --- LatestSource find-by-jid resolution (mirror store TestLatestSource + gated
// TestFindChannelForJID_LatestSourceWins's DB half) ---

// TestLatestSource: the newest non-bot inbound row's source is returned; bot
// rows and missing chats are ignored (gated TestLatestSource / store parity).
func TestLatestSource(t *testing.T) {
	db, _ := targetLoop(t)
	now := time.Now().UTC()
	_ = db.PutMessage(core.Message{ID: "s1", ChatJID: "tg:1", Sender: "alice",
		Content: "hi", Timestamp: now, Source: "tg1"})
	_ = db.PutMessage(core.Message{ID: "s2", ChatJID: "tg:1", Sender: "alice",
		Content: "again", Timestamp: now.Add(time.Second), Source: "tg2"})
	// a later bot row must NOT override the inbound source.
	_ = db.PutMessage(core.Message{ID: "b1", ChatJID: "tg:1", Sender: "bot",
		Content: "reply", Timestamp: now.Add(2 * time.Second), Source: "tg9", BotMsg: true})

	if got := db.LatestSource("tg:1"); got != "tg2" {
		t.Errorf("LatestSource = %q, want tg2 (newest inbound source)", got)
	}
	if got := db.LatestSource("nope"); got != "" {
		t.Errorf("LatestSource(missing) = %q, want empty", got)
	}
}
