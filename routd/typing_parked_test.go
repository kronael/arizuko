package routd

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	apiv1 "github.com/kronael/arizuko/routd/api/v1"
	runedv1 "github.com/kronael/arizuko/runed/api/v1"
	"github.com/kronael/arizuko/store"
)

// holdRunner blocks every Run until release closes.
type holdRunner struct {
	started chan string
	release chan struct{}
}

func (r *holdRunner) Run(_ context.Context, req runedv1.RunRequest) (runedv1.RunOutcome, error) {
	r.started <- req.ChatJID
	<-r.release
	return runedv1.RunOutcome{RunID: "run-hold", Outcome: runedv1.OutcomeOK}, nil
}

func hasTyping(dl *typingDeliverer, jid string, on bool) bool {
	for _, e := range dl.snapshot() {
		if e.jid == jid && e.on == on {
			return true
		}
	}
	return false
}

// A message that waits behind another chat's run in the same folder shows
// typing while it waits (BUGS L663), through the real queue.
func TestTyping_ParkedChatTypesWhileItWaits(t *testing.T) {
	dl := &typingDeliverer{}
	runner := &holdRunner{started: make(chan string, 4), release: make(chan struct{})}
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	loop := NewLoop(db, runner, LoopConfig{Deliver: dl, MaxAgents: 5})
	release := sync.OnceFunc(func() { close(runner.release) })
	t.Cleanup(func() { release(); loop.StopQueue() })
	srv := NewServer(db, loop, dl, nil, 0, "https://example.test")
	const a, b = "slack:T/C/A", "slack:T/C/B"
	parked := make(chan struct{}, 1)
	loop.q.SetParkedFn(func(jid string) {
		loop.noteParked(jid)
		if jid == b {
			parked <- struct{}{}
		}
	})
	finished := make(chan struct{}, 3)
	loop.q.SetProcessMessagesFn(func(jid string) (bool, error) {
		had, err := loop.processGroupMessages(jid)
		finished <- struct{}{}
		return had, err
	})
	seedTypingMsg(t, db, srv, a)
	select {
	case got := <-runner.started:
		if got != a {
			t.Fatalf("first run on %s, want %s", got, a)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("chat A never started")
	}
	rec := doJSON(t, srv.Handler(), "POST", "/v1/messages", "", apiv1.Message{ID: "tm2", ChatJID: b, Sender: "u2", Content: "hello", Verb: "message"})
	if rec.Code != 200 {
		t.Fatalf("ingest status=%d", rec.Code)
	}
	select {
	case <-parked:
	case <-time.After(5 * time.Second):
		t.Fatal("chat B did not park")
	}
	if !hasTyping(dl, b, true) {
		t.Fatalf("chat B waits behind A and shows no typing: events=%+v", dl.snapshot())
	}
	release()
	for range 3 {
		select {
		case <-finished:
		case <-time.After(5 * time.Second):
			t.Fatal("parked work did not drain")
		}
	}
}

// A chat that stops waiting without a run (its batch is dropped) must not
// keep the indicator on.
func TestTyping_ParkedChatDroppedClears(t *testing.T) {
	dl := &typingDeliverer{}
	db, srv := newTypingRoutd(t, &stubRunner{t: t}, dl)
	const jid = "slack:T/C/U1"
	seedTypingMsg(t, db, srv, jid)
	srv.loop.noteParked(jid)
	if !hasTyping(dl, jid, true) {
		t.Fatalf("parked addressed chat shows no typing: %+v", dl.snapshot())
	}
	srv.loop.advance(jid, core.Message{ID: "tm1"})
	if !hasTyping(dl, jid, false) {
		t.Fatalf("dropped chat keeps typing on: %+v", dl.snapshot())
	}
}

// A Busy refusal keeps typing on; the re-dispatch ends it.
func TestTyping_BusyKeepsTypingOn(t *testing.T) {
	dl := &typingDeliverer{}
	sr := &stubRunner{t: t, busy: true}
	db, srv := newTypingRoutd(t, sr, dl)
	sr.srv = srv
	const jid = "slack:T/C/U1"
	seedTypingMsg(t, db, srv, jid)
	if _, err := srv.loop.processGroupMessages(jid); err != nil {
		t.Fatal(err)
	}
	if dl.ons() == 0 || dl.offs() != 0 {
		t.Fatalf("Busy: ons=%d offs=%d, want typing on and never off", dl.ons(), dl.offs())
	}
}

// seedThreadMsg ingests an addressed message posted in thread topic of jid.
func seedThreadMsg(t *testing.T, db *DB, srv *Server, jid, topic string) {
	t.Helper()
	seedTypingMsg(t, db, srv, jid)
	rec := doJSON(t, srv.Handler(), "POST", "/v1/messages", "", apiv1.Message{ID: "tm-thread", ChatJID: jid, Sender: "u1", Content: "in the thread", Verb: "message", Topic: topic})
	if rec.Code != 200 {
		t.Fatalf("ingest status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func hasTypingIn(dl *typingDeliverer, jid, topic string, on bool) bool {
	for _, e := range dl.snapshot() {
		if e.jid == jid && e.topic == topic && e.on == on {
			return true
		}
	}
	return false
}

// A root turn runs while a thread message of the same chat waits: the root
// types in the root, the waiting message in its thread, and the root turn's
// stop leaves the thread's indicator on (BUGS L882).
func TestTyping_WaitingThreadSurvivesRootTurnStop(t *testing.T) {
	dl := &typingDeliverer{}
	db, srv := newTypingRoutd(t, &stubRunner{t: t}, dl)
	const jid, thread = "slack:T/C/U1", "th-1"
	seedThreadMsg(t, db, srv, jid, thread)
	// tm1 (root) is consumed by the running turn; only the thread row waits.
	if err := db.store().AdvanceAgentCursor(jid, store.MessageCursor(core.Message{ID: "tm1"})); err != nil {
		t.Fatal(err)
	}
	srv.loop.beginProgress(jid, "")
	srv.loop.noteParked(jid)
	if !hasTypingIn(dl, jid, thread, true) {
		t.Fatalf("the waiting thread message shows no typing in its thread: %+v", dl.snapshot())
	}
	srv.loop.pauseProgress(jid, "", "")
	if !hasTypingIn(dl, jid, "", false) {
		t.Fatalf("the root turn's stop did not clear the root: %+v", dl.snapshot())
	}
	if hasTypingIn(dl, jid, thread, false) {
		t.Fatalf("the root turn's stop cleared the thread's indicator: %+v", dl.snapshot())
	}
}

// A thread turn types and stops in its thread, never in the chat root.
func TestTyping_ThreadTurnTypesInItsThread(t *testing.T) {
	dl := &typingDeliverer{}
	sr := &stubRunner{t: t}
	db, srv := newTypingRoutd(t, sr, dl)
	sr.srv = srv
	const jid, thread = "slack:T/C/U1", "th-1"
	seedThreadMsg(t, db, srv, jid, thread)
	if _, err := srv.loop.processGroupMessages(jid); err != nil {
		t.Fatal(err)
	}
	if !hasTypingIn(dl, jid, thread, true) || !hasTypingIn(dl, jid, thread, false) {
		t.Fatalf("thread turn did not type on and off in its thread: %+v", dl.snapshot())
	}
}
