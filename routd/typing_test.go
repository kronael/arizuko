package routd

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	apiv1 "github.com/kronael/arizuko/routd/api/v1"
	runedv1 "github.com/kronael/arizuko/runed/api/v1"
)

// typingDeliverer records Typing calls; all other Deliverer methods are no-ops.
type typingDeliverer struct {
	mu     sync.Mutex
	events []typingEvent
}

type typingEvent struct {
	jid string
	on  bool
}

func (d *typingDeliverer) Send(_, _, _, _, _, _ string) (string, error) { return "pid-1", nil }
func (d *typingDeliverer) React(_, _, _ string) error                   { return nil }
func (d *typingDeliverer) Edit(_, _, _ string) error                    { return nil }
func (d *typingDeliverer) Delete(_, _ string) error                     { return nil }
func (d *typingDeliverer) Pin(_, _ string) error                        { return nil }
func (d *typingDeliverer) Unpin(_, _ string, _ bool) error              { return nil }
func (d *typingDeliverer) Document(_, _, _, _, _, _, _ string) (string, error) {
	return "", nil
}
func (d *typingDeliverer) SendVoice(_, _, _, _ string) (string, error)           { return "", nil }
func (d *typingDeliverer) Post(_, _ string, _ []string) (string, error)          { return "", nil }
func (d *typingDeliverer) Forward(_, _, _ string) (string, error)                { return "", nil }
func (d *typingDeliverer) Quote(_, _, _ string) (string, error)                  { return "", nil }
func (d *typingDeliverer) Repost(_, _ string) (string, error)                    { return "", nil }
func (d *typingDeliverer) Dislike(_, _ string) error                             { return nil }
func (d *typingDeliverer) SetSuggestions(_ string, _ []core.PanePrompt) error    { return nil }
func (d *typingDeliverer) SetName(_, _ string) error                             { return nil }
func (d *typingDeliverer) RoundDone(_, _, _, _ string) error                     { return nil }
func (d *typingDeliverer) FetchHistory(_ string, _ time.Time, _ int) ([]byte, error) {
	return nil, nil
}
func (d *typingDeliverer) Typing(jid string, on bool) error {
	d.mu.Lock()
	d.events = append(d.events, typingEvent{jid, on})
	d.mu.Unlock()
	return nil
}

func (d *typingDeliverer) snapshot() []typingEvent {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]typingEvent, len(d.events))
	copy(out, d.events)
	return out
}

func (d *typingDeliverer) ons() int {
	n := 0
	for _, e := range d.snapshot() {
		if e.on {
			n++
		}
	}
	return n
}

func (d *typingDeliverer) offs() int {
	n := 0
	for _, e := range d.snapshot() {
		if !e.on {
			n++
		}
	}
	return n
}

// steeringRunner returns Steered=true immediately. When deliverContent is true
// it also calls appendAndDeliver first, simulating the running container replying.
type steeringRunner struct {
	srv            *Server
	deliverContent bool
}

func (r *steeringRunner) Run(_ context.Context, req runedv1.RunRequest) (runedv1.RunOutcome, error) {
	if r.deliverContent {
		if _, _, row := r.srv.appendAndDeliver(req.TurnID, req.ChatJID, "hello steered", "", true); row != nil {
			_ = r.srv.db.PutMessage(*row)
		}
		first, _ := r.srv.db.RecordTurnResult(string(req.Folder), req.TurnID, "sess-steered", "success")
		if first {
			_ = r.srv.db.SetTurnState(req.TurnID, "done")
		}
	}
	return runedv1.RunOutcome{RunID: "run-steered", Steered: true}, nil
}

func newTypingRoutd(t *testing.T, runner Runner, dl Deliverer) (*DB, *Server) {
	t.Helper()
	db, err := OpenMem()
	if err != nil {
		t.Fatalf("open mem db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	loop := NewLoop(db, runner, LoopConfig{Deliver: dl})
	loop.StopQueue()
	srv := NewServer(db, loop, dl, nil, 0, "https://example.test")
	return db, srv
}

// seedTypingMsg registers a demo group + route and ingests one inbound message.
func seedTypingMsg(t *testing.T, db *DB, srv *Server, jid string) {
	t.Helper()
	h := srv.Handler()
	if err := db.PutGroup(core.Group{Folder: "demo"}); err != nil {
		t.Fatalf("put group: %v", err)
	}
	doJSON(t, h, "PUT", "/v1/routes", "", []apiv1.Route{{Seq: 0, Match: "platform=slack", Target: "demo"}})
	in := apiv1.Message{ID: "tm1", ChatJID: jid, Sender: "u1", Content: "hello", Verb: "message"}
	rec := doJSON(t, h, "POST", "/v1/messages", "", in)
	if rec.Code != 200 {
		t.Fatalf("ingest status=%d body=%s", rec.Code, rec.Body.String())
	}
}

// TestTyping_Normal verifies that a non-steered run with content emits
// Typing(true) before the run and Typing(false) after content lands.
func TestTyping_Normal(t *testing.T) {
	dl := &typingDeliverer{}
	sr := &stubRunner{}
	db, srv := newTypingRoutd(t, sr, dl)
	sr.srv = srv
	const jid = "slack:T/C/U1"
	seedTypingMsg(t, db, srv, jid)

	if _, err := srv.loop.processGroupMessages(jid); err != nil {
		t.Fatalf("process: %v", err)
	}

	evts := dl.snapshot()
	if len(evts) == 0 {
		t.Fatal("no Typing events recorded")
	}
	if !evts[0].on {
		t.Fatalf("first Typing event was off, want on")
	}
	if dl.offs() == 0 {
		t.Fatal("Typing(false) never fired after non-steered run with content")
	}
}

// TestTyping_Steered_NoImmediateOff verifies that when dispatchRun returns
// Steered=true without delivering content, Typing(false) is NOT called.
// Typing stays alive until the agent eventually replies (maxTTL safety net in
// TypingRefresher covers the case it never does).
func TestTyping_Steered_NoImmediateOff(t *testing.T) {
	dl := &typingDeliverer{}
	runner := &steeringRunner{deliverContent: false}
	db, srv := newTypingRoutd(t, runner, dl)
	runner.srv = srv
	const jid = "slack:T/C/U2"
	seedTypingMsg(t, db, srv, jid)

	if _, err := srv.loop.processGroupMessages(jid); err != nil {
		t.Fatalf("process: %v", err)
	}

	if dl.offs() > 0 {
		t.Fatalf("Typing(false) called %d times on steered turn without content, want 0", dl.offs())
	}
	if dl.ons() == 0 {
		t.Fatal("Typing(true) was not called before steered run")
	}
}

// TestTyping_Steered_ClearsOnContent verifies that when a steered run delivers
// content via appendAndDeliver, Typing(false) fires at content-delivery time
// and not from the dispatch path.
func TestTyping_Steered_ClearsOnContent(t *testing.T) {
	dl := &typingDeliverer{}
	runner := &steeringRunner{deliverContent: true}
	db, srv := newTypingRoutd(t, runner, dl)
	runner.srv = srv
	const jid = "slack:T/C/U3"
	seedTypingMsg(t, db, srv, jid)

	if _, err := srv.loop.processGroupMessages(jid); err != nil {
		t.Fatalf("process: %v", err)
	}

	if dl.offs() == 0 {
		t.Fatal("Typing(false) not called after steered run delivered content")
	}
	// The off must follow the on.
	sawOn := false
	for _, e := range dl.snapshot() {
		if e.on {
			sawOn = true
		}
		if !e.on && !sawOn {
			t.Fatal("Typing(false) fired before Typing(true)")
		}
	}
}
