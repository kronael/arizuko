package routd

// poll_load_test.go pins BUGS L821: routd reads a chat only when something
// wakes it. A 2 s poll over every chat saturated the shared store on hel1v5.

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
	apiv1 "github.com/kronael/arizuko/routd/api/v1"
	runedv1 "github.com/kronael/arizuko/runed/api/v1"
	"github.com/kronael/arizuko/store"
)

type countingStore struct {
	objstore.Store
	mu    sync.Mutex
	reads map[string]int
}

func (c *countingStore) Query(ctx context.Context, q objstore.Query) ([]objstore.Item, error) {
	c.mu.Lock()
	c.reads[q.Entity]++
	if q.Key.Op == objstore.AnyKey {
		c.reads[q.Entity+" whole"]++
	}
	c.mu.Unlock()
	return c.Store.Query(ctx, q)
}

func (c *countingStore) count(key string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.reads[key]
}

func (c *countingStore) total() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for k, v := range c.reads {
		if !strings.HasSuffix(k, " whole") {
			n += v
		}
	}
	return n
}

// countedLoop is a loop over a store that counts every read, the queue stopped.
func countedLoop(t *testing.T) (*DB, *Loop, *countingStore) {
	t.Helper()
	base, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { base.Close() })
	counter := &countingStore{Store: base.obj, reads: map[string]int{}}
	db := OpenOver(counter)
	l := NewLoop(db, nopRunner{}, LoopConfig{})
	l.StopQueue()
	return db, l, counter
}

// A wake reads the turn_context partition whole once (held input), not twice:
// legacy retry migration runs in recovery, not on every wake. gitd re-delivers
// its held CI events one wake each.
func TestWakeReadsTurnContextOnce(t *testing.T) {
	db, l, counter := countedLoop(t)
	_ = db.PutGroup(core.Group{Folder: "eng"})
	doSetRoutes(t, db, []core.Route{{Match: "platform=github", Target: "eng?observe"}})
	srv := NewServer(db, l, nil, nil, 0, "")
	clear(counter.reads)
	const jid = "github:o/r"
	if _, err := srv.ingestMessage(apiv1.Message{ID: "gh-1", ChatJID: jid,
		Sender: "github:ci", Content: "[github kind=check_run] tests passed",
		Topic: "pr/1"}, "", ""); err != nil {
		t.Fatal(err)
	}
	ingest := counter.total()
	if _, err := l.processGroupMessages(jid); err != nil {
		t.Fatal(err)
	}
	t.Logf("store reads: ingest %d, wake %d, turn_context whole %d",
		ingest, counter.total()-ingest, counter.count(store.TurnContexts+" whole"))
	if got := counter.count(store.TurnContexts + " whole"); got != 1 {
		t.Fatalf("whole turn_context reads per wake = %d, want 1", got)
	}
}

func (c *countingStore) chatReads() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.reads[store.Chats] + c.reads[store.Messages]
}

// The loop sweeps the chats once at startup and then reads none of them, however
// many ticks pass: krons asked 471 queries per 2 s tick over 155 chats.
func TestTickReadsNoChat(t *testing.T) {
	base, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	counter := &countingStore{Store: base.obj, reads: map[string]int{}}
	db := OpenOver(counter)
	const chats = 5
	for i := range chats {
		m := core.Message{ID: fmt.Sprintf("m%d", i), ChatJID: fmt.Sprintf("slack:T/C%d/U", i),
			Sender: "u", Content: "old", Timestamp: time.Now().UTC(), Verb: "message"}
		if err := db.PutMessage(m); err != nil {
			t.Fatal(err)
		}
		if err := db.SetAgentCursor(m.ChatJID, store.MessageCursor(m)); err != nil {
			t.Fatal(err)
		}
	}
	l := NewLoop(db, nopRunner{}, LoopConfig{PollEvery: 5 * time.Millisecond})
	counter.mu.Lock()
	clear(counter.reads)
	counter.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	l.Run(ctx)

	// ChatJIDs once, then a cursor and a message read per chat.
	if got, startup := counter.chatReads(), 1+2*chats; got > startup {
		t.Fatalf("chat and message reads = %d over many ticks, want at most the startup sweep's %d", got, startup)
	}
}

// A pass that fails is retried after the backoff, not left for the next message.
func TestFailedPassIsRetried(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		q := newTurnControl(1)
		var passes atomic.Int32
		q.SetProcessMessagesFn(func(string) (bool, error) {
			if passes.Add(1) == 1 {
				return false, errors.New("store timed out")
			}
			return false, nil
		})
		q.EnqueueMessageCheck("chat")
		time.Sleep(busyBackoffBase + time.Second)
		synctest.Wait()
		if got := passes.Load(); got != 2 {
			t.Fatalf("passes = %d, want 2: the failed pass was not retried", got)
		}
		q.Shutdown()
	})
}

// chanRunner reports each run on runs; with busyFirst, runed refuses the first.
type chanRunner struct {
	runs      chan string
	busyFirst bool
	n         atomic.Int32
}

func (r *chanRunner) Run(_ context.Context, req runedv1.RunRequest) (runedv1.RunOutcome, error) {
	r.runs <- req.TurnID
	if r.n.Add(1) == 1 && r.busyFirst {
		return runedv1.RunOutcome{Busy: true}, nil
	}
	return runedv1.RunOutcome{RunID: "r", Outcome: runedv1.OutcomeOK}, nil
}

// A Busy refusal leaves the batch unconsumed; the chat wakes again after the
// backoff and the batch runs.
func TestBusyRefusalWakesTheChatAgain(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_ = db.PutGroup(core.Group{Folder: "demo"})
	runner := &chanRunner{runs: make(chan string, 4), busyFirst: true}
	l := NewLoop(db, runner, LoopConfig{})
	defer l.StopQueue()
	if err := db.PutMessage(core.Message{ID: "a", ChatJID: "web:demo", Sender: "u",
		Content: "q", Timestamp: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	l.Enqueue("web:demo")
	for i := range 2 {
		select {
		case <-runner.runs:
		case <-time.After(busyBackoffBase + 5*time.Second):
			t.Fatalf("run %d never came: the Busy refusal was not re-fed", i+1)
		}
	}
}

// An agent's inject_message or send to a group wakes the target chat itself.
func TestAgentMessageWakesTheChat(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_ = db.PutGroup(core.Group{Folder: "demo"})
	runner := &chanRunner{runs: make(chan string, 4)}
	l := NewLoop(db, runner, LoopConfig{})
	defer l.StopQueue()
	srv := NewServer(db, l, nil, nil, 0, "")
	if err := srv.putAgentMessage(turnMCP{}, core.Message{ID: "in-1", ChatJID: "web:demo",
		Sender: "u", Content: "q", Timestamp: time.Now().UTC(), Verb: "message"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-runner.runs:
	case <-time.After(5 * time.Second):
		t.Fatal("the injected message never ran: nothing woke its chat")
	}
}

// gatedStore holds the owed-status read until the gate opens: a store too slow
// to answer it.
type gatedStore struct {
	objstore.Store
	gate chan struct{}
}

func (g *gatedStore) Query(ctx context.Context, q objstore.Query) ([]objstore.Item, error) {
	if q.Entity == store.TurnContexts && slices.ContainsFunc(q.Where, func(c objstore.Cmp) bool { return c.Attr == "status_owed" }) {
		<-g.gate
	}
	return g.Store.Query(ctx, q)
}

// A restart dispatches the input written while routd was down however long the
// owed-status read takes: on krons the startup recovery held the chat sweep
// (BUGS L825).
func TestStartupSweepDoesNotWaitForOwedStatuses(t *testing.T) {
	base, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	_ = base.PutGroup(core.Group{Folder: "demo"})
	if err := base.PutMessage(core.Message{ID: "a", ChatJID: "web:demo", Sender: "u",
		Content: "q", Timestamp: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	gated := &gatedStore{Store: base.obj, gate: make(chan struct{})}
	runner := &chanRunner{runs: make(chan string, 4)}
	l := NewLoop(OpenOver(gated), runner, LoopConfig{Deliver: &recDeliverer{}})
	ctx, cancel := context.WithCancel(context.Background())
	go l.Run(ctx)
	defer func() {
		close(gated.gate)
		cancel()
		l.Wait(10 * time.Second)
	}()
	select {
	case <-runner.runs:
	case <-time.After(10 * time.Second):
		t.Fatal("the swept chat never ran: startup waited on the owed-status read")
	}
}

// A chat the open breaker refuses runs once the probe is due, with no new
// message to wake it: krons' CLI message waited behind a breaker that rhias/nemo
// opened, and nothing woke its chat again (BUGS L824).
func TestBreakerRefusedChatRunsWhenDue(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_ = db.PutGroup(core.Group{Folder: "demo"})
	now := time.Now().UTC()
	open := store.TurnControlState{Phase: "open", Episode: "crash", NextProbeAt: now.Add(time.Hour)}
	if ok, err := db.store().WriteTurnControl(t.Context(), nil, open); err != nil || !ok {
		t.Fatalf("open the breaker: %v %v", ok, err)
	}
	runner := &chanRunner{runs: make(chan string, 4)}
	l := NewLoop(db, runner, LoopConfig{})
	defer l.StopQueue()
	var clock atomic.Int64
	clock.Store(now.UnixNano())
	l.q.now = func() time.Time { return time.Unix(0, clock.Load()) }
	if err := db.PutMessage(core.Message{ID: "a", ChatJID: "web:demo", Sender: "u",
		Content: "q", Timestamp: now}); err != nil {
		t.Fatal(err)
	}
	if _, err := l.processGroupMessages("web:demo"); err != nil {
		t.Fatal(err)
	}
	select {
	case id := <-runner.runs:
		t.Fatalf("turn %s ran through an open breaker", id)
	default:
	}
	isWoken := func() bool {
		l.q.mu.Lock()
		defer l.q.mu.Unlock()
		s := l.q.getGroup("web:demo")
		return s.active || !s.refused
	}
	l.q.tick(l)
	if isWoken() {
		t.Fatal("the tick woke the chat before the probe was due")
	}
	clock.Store(open.NextProbeAt.Add(time.Second).UnixNano())
	probe := open
	probe.Phase = "probe"
	if ok, err := db.store().WriteTurnControl(t.Context(), &open, probe); err != nil || !ok {
		t.Fatalf("start a probe: %v %v", ok, err)
	}
	l.q.tick(l)
	if isWoken() {
		t.Fatal("the tick woke the chat while another turn probes")
	}
	if ok, err := db.store().WriteTurnControl(t.Context(), &probe, open); err != nil || !ok {
		t.Fatalf("reopen the breaker: %v %v", ok, err)
	}
	l.q.tick(l)
	select {
	case <-runner.runs:
	case <-time.After(10 * time.Second):
		t.Fatal("the refused chat never ran once the probe was due")
	}
}

// partitionStore counts the message reads of each chat.
type partitionStore struct {
	objstore.Store
	mu    sync.Mutex
	chats map[string]int
}

func (p *partitionStore) Query(ctx context.Context, q objstore.Query) ([]objstore.Item, error) {
	if q.Entity == store.Messages {
		p.mu.Lock()
		p.chats[q.Partition]++
		p.mu.Unlock()
	}
	return p.Store.Query(ctx, q)
}

// A dispatch, here the probe of a due breaker, reads no other chat: ordering
// model input and choosing the probe each read every chat's pending batch, 155
// on krons, per dispatch (BUGS L824).
func TestDispatchReadsNoOtherChat(t *testing.T) {
	base, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	counter := &partitionStore{Store: base.obj, chats: map[string]int{}}
	db := OpenOver(counter)
	_ = db.PutGroup(core.Group{Folder: "demo"})
	now := time.Now().UTC()
	for i := range 5 {
		if err := db.PutMessage(core.Message{ID: fmt.Sprintf("o%d", i), ChatJID: fmt.Sprintf("web:other%d", i),
			Sender: "u", Content: "q", Timestamp: now}); err != nil {
			t.Fatal(err)
		}
	}
	open := store.TurnControlState{Phase: "open", Episode: "crash", NextProbeAt: now.Add(-time.Second)}
	if ok, err := db.store().WriteTurnControl(t.Context(), nil, open); err != nil || !ok {
		t.Fatalf("open the breaker: %v %v", ok, err)
	}
	if err := db.PutMessage(core.Message{ID: "a", ChatJID: "web:demo", Sender: "u",
		Content: "q", Timestamp: now}); err != nil {
		t.Fatal(err)
	}
	runner := &chanRunner{runs: make(chan string, 4)}
	l := NewLoop(db, runner, LoopConfig{})
	defer l.StopQueue()
	counter.mu.Lock()
	clear(counter.chats)
	counter.mu.Unlock()
	if _, err := l.processGroupMessages("web:demo"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-runner.runs:
	default:
		t.Fatal("the due probe did not run")
	}
	counter.mu.Lock()
	defer counter.mu.Unlock()
	for jid, n := range counter.chats {
		if jid != "web:demo" {
			t.Fatalf("the dispatch read chat %s %d times: %v", jid, n, counter.chats)
		}
	}
}

// A batch held behind its folder's older retry marks its chat for the tick to
// wake: nothing else re-enqueues it once the retry has run (BUGS L824).
func TestHeldModelInputMarksTheChatRefused(t *testing.T) {
	db, l, runner, tc, now := deferredFixture(t)
	doSetRoutes(t, db, []core.Route{{Match: "chat_jid=telegram:group/9", Target: "demo"}})
	if err := db.PutMessage(core.Message{ID: "newer", ChatJID: "telegram:group/9", Sender: "u",
		Content: "q", Timestamp: now.Add(time.Second)}); err != nil {
		t.Fatal(err)
	}
	if _, err := l.processGroupMessages("telegram:group/9"); err != nil {
		t.Fatal(err)
	}
	l.q.mu.Lock()
	refused := l.q.getGroup("telegram:group/9").refused
	l.q.mu.Unlock()
	if len(runner.runs) != 0 || !refused {
		t.Fatalf("held behind retry %s: runs=%d refused=%v, want no run and a refused chat",
			tc.TurnID, len(runner.runs), refused)
	}
}
