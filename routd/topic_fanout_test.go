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
