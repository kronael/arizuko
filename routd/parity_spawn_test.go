package routd

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
)

// Tier-2 parity: spawn.go rollback + observeWindow override + recoverPending
// re-enqueue + LatestSource resolution, all shipped but under-tested vs gated.

// --- spawnFromPrototype AddRoute-error rollback (mirror gateway_extra) ---

// TestSpawnFromPrototype_AddRouteError: when the route insert fails, the child
// group row is rolled back so no route-less, un-respawnable orphan is left
// behind (gated TestSpawnFromPrototype_AddRouteError).
func TestSpawnFromPrototype_AddRouteError(t *testing.T) {
	db, l, groups := promptLoop(t)
	parentFolder := "main"
	protoDir := filepath.Join(groups, parentFolder, "prototype")
	if err := os.MkdirAll(protoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(protoDir, "CLAUDE.md"), []byte("# proto"), 0o644)
	_ = db.PutGroup(core.Group{Folder: parentFolder, AddedAt: time.Now().UTC(),
		Config: core.GroupConfig{MaxChildren: 5}})

	// Drop the routes table so PutGroup succeeds but AddRoute fails.
	if _, err := db.SQL().Exec("DROP TABLE routes"); err != nil {
		t.Fatalf("drop routes: %v", err)
	}

	_, err := l.spawnFromPrototype(parentFolder, "telegram:888")
	if err == nil {
		t.Fatal("spawnFromPrototype swallowed AddRoute error; child would be orphaned")
	}
	if !strings.Contains(err.Error(), "add route") {
		t.Errorf("error = %q, want 'add route' context", err.Error())
	}
	// The child group row must be rolled back — no un-respawnable orphan.
	if _, ok := db.GroupByFolder(spawnFolderName(parentFolder, "telegram:888")); ok {
		t.Fatal("AddRoute failure left an orphan child group row (no rollback)")
	}
}

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

// --- recoverPending re-enqueue (mirror gateway_test:TestRecoverPendingMessages) ---

// TestRecoverPending_ReEnqueuesRunningTurns: on boot, recoverPending re-feeds
// every chat with a still-'running' turn_context so a crash mid-turn re-attempts
// from the un-advanced cursor (turn_results dedups the old run's submit_turn).
// Split twin of gated recoverPendingMessages — gated scans pending message rows;
// routd re-enqueues running turns (the split owns turn_context, not a pending
// flag), but the recovered SET of chats is the parity invariant.
func TestRecoverPending_ReEnqueuesRunningTurns(t *testing.T) {
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
	// t3 reached a terminal state — RunningTurns must exclude it.
	if _, err := db.RecordTurnResult("demo", "t3", "s", "success"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL().Exec("UPDATE turn_context SET state='done' WHERE turn_id='t3'"); err != nil {
		t.Fatal(err)
	}

	l.recoverPending()

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
		t.Error("recoverPending re-fed a chat whose turn was already terminal")
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
