package routd

import (
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
)

// Two live threads in one Discord channel, addressed by the SAME person: each
// one is its own conversation and each one is owed its own turn. Grouping by
// sender alone put both mentions in one turn whose topic was the LAST row's, so
// the first thread was answered inside the second and then went silent for
// three weeks on sloth (BUGS L305).
func TestDispatch_TwoThreadsOneSenderGetOneTurnEach(t *testing.T) {
	db, loop, rr := recLoop(t)
	_ = db.PutGroup(core.Group{Folder: "desk"})
	doSetRoutes(t, db, []core.Route{{Match: "platform=discord verb=mention", Target: "desk"}})
	now := time.Now().UTC()
	const jid = "discord:G/C"
	_ = db.PutMessage(core.Message{ID: "t1", ChatJID: jid, Sender: "u", Topic: "thread-a",
		Content: "first thread", Timestamp: now, Verb: "mention"})
	_ = db.PutMessage(core.Message{ID: "t2", ChatJID: jid, Sender: "u", Topic: "thread-b",
		Content: "second thread", Timestamp: now.Add(time.Second), Verb: "mention"})

	if _, err := loop.processGroupMessages(jid); err != nil {
		t.Fatalf("process: %v", err)
	}
	if len(rr.runs) != 2 {
		t.Fatalf("turns=%d want 2 (one per thread): %+v", len(rr.runs), rr.runs)
	}
	if rr.runs[0].topic != "thread-a" || rr.runs[1].topic != "thread-b" {
		t.Fatalf("topics=[%q %q] want [thread-a thread-b]", rr.runs[0].topic, rr.runs[1].topic)
	}
	if !contains(rr.runs[0].batch, "first thread") || contains(rr.runs[0].batch, "second thread") {
		t.Fatalf("thread-a's turn carries the wrong rows: %q", rr.runs[0].batch)
	}
	if !contains(rr.runs[1].batch, "second thread") || contains(rr.runs[1].batch, "first thread") {
		t.Fatalf("thread-b's turn carries the wrong rows: %q", rr.runs[1].batch)
	}
}

// A route that PINS a topic (`target#pinned`) gives every row in the chat one
// destination, so the topic half of the grouping key collapses and the chat
// still takes exactly one turn. Without that collapse a pinned chat would fan
// out one turn per incoming thread and every one of them would run in the same
// pinned topic.
func TestDispatch_ARoutePinnedTopicStillTakesOneTurn(t *testing.T) {
	db, loop, rr := recLoop(t)
	_ = db.PutGroup(core.Group{Folder: "desk"})
	doSetRoutes(t, db, []core.Route{{Match: "platform=discord verb=mention", Target: "desk#pinned"}})
	now := time.Now().UTC()
	const jid = "discord:G/P"
	_ = db.PutMessage(core.Message{ID: "p1", ChatJID: jid, Sender: "u", Topic: "thread-a",
		Content: "first thread", Timestamp: now, Verb: "mention"})
	_ = db.PutMessage(core.Message{ID: "p2", ChatJID: jid, Sender: "u", Topic: "thread-b",
		Content: "second thread", Timestamp: now.Add(time.Second), Verb: "mention"})

	if _, err := loop.processGroupMessages(jid); err != nil {
		t.Fatalf("process: %v", err)
	}
	if len(rr.runs) != 1 {
		t.Fatalf("turns=%d want 1 (the route pins one topic): %+v", len(rr.runs), rr.runs)
	}
	if rr.runs[0].topic != "pinned" {
		t.Fatalf("topic=%q want pinned", rr.runs[0].topic)
	}
}

// A sticky #topic is the operator's own pin on the chat, and it collapses the
// key the same way the route pin does.
func TestDispatch_AStickyTopicStillTakesOneTurn(t *testing.T) {
	db, loop, rr := recLoop(t)
	_ = db.PutGroup(core.Group{Folder: "desk"})
	doSetRoutes(t, db, []core.Route{{Match: "platform=discord verb=mention", Target: "desk"}})
	const jid = "discord:G/S"
	if err := db.SetStickyTopic(jid, "stuck"); err != nil {
		t.Fatalf("sticky: %v", err)
	}
	now := time.Now().UTC()
	_ = db.PutMessage(core.Message{ID: "s1", ChatJID: jid, Sender: "u", Topic: "thread-a",
		Content: "first thread", Timestamp: now, Verb: "mention"})
	_ = db.PutMessage(core.Message{ID: "s2", ChatJID: jid, Sender: "u", Topic: "thread-b",
		Content: "second thread", Timestamp: now.Add(time.Second), Verb: "mention"})

	if _, err := loop.processGroupMessages(jid); err != nil {
		t.Fatalf("process: %v", err)
	}
	if len(rr.runs) != 1 {
		t.Fatalf("turns=%d want 1 (a sticky topic pins the chat): %+v", len(rr.runs), rr.runs)
	}
	if rr.runs[0].topic != "stuck" {
		t.Fatalf("topic=%q want stuck", rr.runs[0].topic)
	}
}

// L336 made steerRowIdx pick the OLDEST slash command, and that row also fed
// route resolution. On a mention-only route, a batch of `/ping` then `@bot …`
// resolved on the COMMAND's verb, missed the route, and routeMiss advanced the
// cursor over BOTH rows — the mention was lost for good. That is worse than
// the text-passthrough L336 set out to fix.
//
// The correct behaviour is not "one turn now": a steer consumes its own row and
// no other, so the mention stays ABOVE the cursor and takes the next poll. This
// drives processGroupMessages twice, because the defect was in which row each
// question reads and only the wiring shows it.
func TestDispatch_ACommandBeforeAMentionStillRoutes(t *testing.T) {
	db, loop, rr := recLoop(t)
	_ = db.PutGroup(core.Group{Folder: "desk"})
	doSetRoutes(t, db, []core.Route{{Match: "platform=discord verb=mention", Target: "desk"}})
	now := time.Now().UTC()
	const jid = "discord:G/C"
	// The command carries verb=message — it matches no mention-only route.
	_ = db.PutMessage(core.Message{ID: "c1", ChatJID: jid, Sender: "u",
		Content: "/ping", Timestamp: now, Verb: "message"})
	_ = db.PutMessage(core.Message{ID: "c2", ChatJID: jid, Sender: "u",
		Content: "@bot and also look at this", Timestamp: now.Add(time.Second), Verb: "mention"})

	if _, err := loop.processGroupMessages(jid); err != nil {
		t.Fatalf("first poll: %v", err)
	}
	if len(rr.runs) != 0 {
		t.Fatalf("first poll ran %d turns, want 0: the command is steered and "+
			"consumes only its own row: %+v", len(rr.runs), rr.runs)
	}
	// The mention was never consumed, so the next poll must dispatch it. Before
	// the fix the route missed here and routeMiss walked the cursor past it.
	if _, err := loop.processGroupMessages(jid); err != nil {
		t.Fatalf("second poll: %v", err)
	}
	if len(rr.runs) != 1 {
		t.Fatalf("second poll ran %d turns, want 1: the mention must still "+
			"dispatch after a command in front of it: %+v", len(rr.runs), rr.runs)
	}
}

func TestDispatch_GitHubRepositoriesHaveDistinctSessions(t *testing.T) {
	db, loop, rr := recLoop(t)
	if err := db.PutGroup(core.Group{Folder: "desk"}); err != nil {
		t.Fatal(err)
	}
	doSetRoutes(t, db, []core.Route{{Match: "platform=github verb=mention", Target: "desk#{jid}"}})
	jids := []string{"github:org/first/pr/12", "github:org/second/pr/12"}
	for i, jid := range jids {
		if err := db.SetStickyTopic(jid, "sticky"); err != nil {
			t.Fatal(err)
		}
		if err := db.PutMessage(core.Message{ID: jid, ChatJID: jid, Sender: "user", Verb: "mention", Content: "review", Timestamp: time.Now().Add(time.Duration(i) * time.Second)}); err != nil {
			t.Fatal(err)
		}
		if _, err := loop.processGroupMessages(jid); err != nil {
			t.Fatal(err)
		}
	}
	if len(rr.runs) != 2 || rr.runs[0].topic != jids[0] || rr.runs[1].topic != jids[1] {
		t.Fatalf("runs = %+v", rr.runs)
	}
	a, okA := db.GetSession("desk", jids[0])
	b, okB := db.GetSession("desk", jids[1])
	if !okA || !okB || a == "" || b == "" || a == b {
		t.Fatalf("sessions = %q/%v, %q/%v", a, okA, b, okB)
	}
}

func TestRouteTopicControlsEngagementAndObservation(t *testing.T) {
	for _, tc := range []struct {
		mode, verb string
		engaged    bool
	}{
		{"", "mention", false}, {"?observe", "message", false}, {"?engaged", "check_run", true},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			db, loop, _ := recLoop(t)
			if err := db.PutGroup(core.Group{Folder: "desk"}); err != nil {
				t.Fatal(err)
			}
			doSetRoutes(t, db, []core.Route{{Match: "platform=github", Target: "desk" + tc.mode + "#{jid}"}})
			jid := "github:org/repo/pr/12"
			if err := db.SetStickyTopic(jid, "sticky"); err != nil {
				t.Fatal(err)
			}
			if tc.engaged {
				if err := db.SetEngagement(jid, jid, "desk", time.Hour); err != nil {
					t.Fatal(err)
				}
			}
			if err := db.PutMessage(core.Message{ID: "row", ChatJID: jid, Sender: "user", Verb: tc.verb, Content: "event", Timestamp: time.Now()}); err != nil {
				t.Fatal(err)
			}
			m, ok := db.MessageByID(jid, "row")
			if !ok || m.Topic != jid {
				t.Fatalf("stored = %+v, %v", m, ok)
			}
			got := loop.resolve(jid, m)
			if tc.mode == "?observe" {
				if got.Observe != "desk" {
					t.Fatalf("resolution = %+v", got)
				}
				loop.routeMiss(jid, []core.Message{m}, got)
				observed := db.ObservedSince("desk", "", 10, 10000)
				if len(observed) != 1 || observed[0].Topic != jid {
					t.Fatalf("observed = %+v", observed)
				}
			} else if !got.ok || got.Folder != "desk" {
				t.Fatalf("resolution = %+v", got)
			}
		})
	}
}
