package chanlib

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kronael/arizuko/chanlib/chanstore"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore/dynstore"
)

// platform stands in for the two calls an adapter hands AckMark. It keeps the
// set of marks that stand on the "platform", so a body asserts what a user of
// the platform would see, not only which calls were made.
type platform struct {
	mu        sync.Mutex
	standing  map[string]string // handle → target
	calls     []string
	next      int
	addErr    error
	removeErr error
}

func newPlatform() *platform { return &platform{standing: map[string]string{}} }

func (p *platform) add(jid, target string) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, "add "+target)
	if p.addErr != nil {
		return "", p.addErr
	}
	p.next++
	h := fmt.Sprintf("h%d", p.next)
	p.standing[h] = target
	return h, nil
}

func (p *platform) remove(jid, target, handle string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, "remove "+target+" "+handle)
	if p.removeErr != nil {
		return p.removeErr
	}
	delete(p.standing, handle)
	return nil
}

func (p *platform) seen() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return strings.Join(p.calls, "; ")
}

func (p *platform) targets() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []string
	for _, t := range p.standing {
		out = append(out, t)
	}
	return out
}

const markJID = "github:o/r/pr/1"

// newMark builds a helper over st attached to a client whose routd answers 200.
func newMark(t *testing.T, st *chanstore.Store, p *platform) (*AckMark, *RouterClient) {
	t.Helper()
	_, srv := newRoutdStub(200)
	t.Cleanup(srv.Close)
	rc := NewRouterClient(srv.URL)
	m := NewAckMark(rc, st, p.add, p.remove)
	t.Cleanup(m.Stop)
	return m, rc
}

func inbound(t *testing.T, rc *RouterClient, id, verb string) {
	t.Helper()
	if err := rc.SendMessage(InboundMsg{ID: id, ChatJID: markJID, Verb: verb}); err != nil {
		t.Fatalf("deliver %s: %v", id, err)
	}
}

// The end of a turn arrives after the adapter restarted. The mark set by the
// first process is found again from the store and removed by the second.
func TestAckMark_PersistsAndSurvivesRestart(t *testing.T) {
	st := testStore(t)
	p := newPlatform()

	first, rc := newMark(t, st, p)
	inbound(t, rc, "gh-c1", "mention")
	first.Typing(markJID, true)
	if got := p.targets(); len(got) != 1 || got[0] != "gh-c1" {
		t.Fatalf("after on the platform shows %v, want the mark on gh-c1", got)
	}

	// The process dies here: nothing calls Stop, the helper is simply gone.
	second, _ := newMark(t, st, p)
	second.Typing(markJID, false)

	if got := p.targets(); len(got) != 0 {
		t.Errorf("a fresh instance left %v standing; calls: %s", got, p.seen())
	}
	if _, held, _ := st.MarkFor(t.Context(), markJID); held {
		t.Error("the record outlived the removal")
	}
}

// A mark older than the backstop was left by a crash; a younger one belongs to
// a turn that may still be running and is kept.
func TestAckMark_BootSweepRemovesStaleMarks(t *testing.T) {
	st := testStore(t)
	p := newPlatform()
	m, _ := newMark(t, st, p)

	stale, fresh := "github:o/r/pr/1", "github:o/r/pr/2"
	old := core.TS(time.Now().Add(-2 * m.ttl))
	for jid, at := range map[string]string{stale: old, fresh: core.NowTS()} {
		h, _ := p.add(jid, "gh-c-"+jid[len(jid)-1:])
		if err := st.SetMark(t.Context(), jid, "gh-c-"+jid[len(jid)-1:], h, at); err != nil {
			t.Fatal(err)
		}
	}

	m.Sweep(t.Context())

	if got := p.targets(); len(got) != 1 || got[0] != "gh-c-2" {
		t.Errorf("after the sweep the platform shows %v, want only the fresh mark gh-c-2", got)
	}
	marks, _ := st.Marks(t.Context())
	if len(marks) != 1 || marks[0].ChatJID != fresh {
		t.Errorf("records left = %+v, want only the fresh one", marks)
	}
}

// A mark the sweep keeps still gets its backstop, for the time it has left.
// Without it a mark whose off never arrives would stand until the next boot.
func TestAckMark_BootSweepRearmsTheBackstopOfAFreshMark(t *testing.T) {
	st := testStore(t)
	p := newPlatform()
	m, _ := newMark(t, st, p)
	m.ttl = 150 * time.Millisecond

	h, _ := p.add(markJID, "gh-c1")
	if err := st.SetMark(t.Context(), markJID, "gh-c1", h, core.NowTS()); err != nil {
		t.Fatal(err)
	}
	m.Sweep(t.Context())
	if len(p.targets()) != 1 {
		t.Fatal("the sweep removed a mark younger than the backstop")
	}
	waitUntil(t, func() bool { return len(p.targets()) == 0 }, "the backstop never removed the swept mark")
	m.mu.Lock()
	_, held, err := st.MarkFor(t.Context(), markJID)
	m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if held {
		t.Error("the backstop left the record behind")
	}
}

// The mark comes off the message it was set on, even when newer messages
// arrived while the agent worked.
func TestAckMark_OffTargetsTheMarkedMessageNotTheLatest(t *testing.T) {
	st := testStore(t)
	p := newPlatform()
	m, rc := newMark(t, st, p)

	inbound(t, rc, "m1", "message")
	m.Typing(markJID, true)
	inbound(t, rc, "m2", "message")
	inbound(t, rc, "m3", "mention")
	m.Typing(markJID, false)

	if want := "add m1; remove m1 h1"; p.seen() != want {
		t.Errorf("calls = %q, want %q", p.seen(), want)
	}
}

// Only a message that can start a turn retargets the mark.
func TestAckMark_OnlyTurnVerbsAreTriggers(t *testing.T) {
	st := testStore(t)
	p := newPlatform()
	m, rc := newMark(t, st, p)

	inbound(t, rc, "m1", "")
	inbound(t, rc, "r1", "like")
	inbound(t, rc, "j1", "join")
	m.Typing(markJID, true)

	if want := "add m1"; p.seen() != want {
		t.Errorf("calls = %q, want %q", p.seen(), want)
	}
}

// A chat with no recorded trigger, or one the platform cannot mark, gets no
// mark and no record.
func TestAckMark_NoTargetSetsNothing(t *testing.T) {
	st := testStore(t)
	p := newPlatform()
	m, rc := newMark(t, st, p)

	m.Typing(markJID, true)
	if p.seen() != "" {
		t.Errorf("with no trigger the platform was called: %s", p.seen())
	}

	p.addErr = ErrNoMarkTarget
	inbound(t, rc, "gh-r7", "message")
	m.Typing(markJID, true)
	if marks, _ := st.Marks(t.Context()); len(marks) != 0 {
		t.Errorf("a refused target was recorded: %+v", marks)
	}
}

// One mark per chat: the second on takes the first off before it adds.
func TestAckMark_SecondOnReplacesTheFirst(t *testing.T) {
	st := testStore(t)
	p := newPlatform()
	m, rc := newMark(t, st, p)

	inbound(t, rc, "m1", "message")
	m.Typing(markJID, true)
	inbound(t, rc, "m2", "message")
	m.Typing(markJID, true)

	if want := "add m1; remove m1 h1; add m2"; p.seen() != want {
		t.Errorf("calls = %q, want %q", p.seen(), want)
	}
	if got := p.targets(); len(got) != 1 || got[0] != "m2" {
		t.Errorf("platform shows %v, want only m2", got)
	}
}

// A refused removal keeps the record, so the next sweep can try again; the
// record is dropped only once the platform confirmed.
func TestAckMark_FailedRemovalKeepsTheRecord(t *testing.T) {
	st := testStore(t)
	p := newPlatform()
	m, rc := newMark(t, st, p)

	inbound(t, rc, "m1", "message")
	m.Typing(markJID, true)
	p.removeErr = errors.New("403")
	m.Typing(markJID, false)
	if _, held, _ := st.MarkFor(t.Context(), markJID); !held {
		t.Fatal("the record was dropped although the platform still shows the mark")
	}

	p.removeErr = nil
	m.ttl = 0 // everything recorded is now stale
	m.Sweep(t.Context())
	if len(p.targets()) != 0 {
		t.Errorf("the next sweep left %v standing", p.targets())
	}
}

// A mark that cannot be recorded cannot be found again, so it does not stay.
func TestAckMark_UnrecordableMarkIsTakenOff(t *testing.T) {
	testStore(t)
	driver, err := dynstore.FromEnv(dynstore.Prefix("testadapter"))
	if err != nil {
		t.Fatal(err)
	}
	p := newPlatform()
	_, srv := newRoutdStub(200)
	defer srv.Close()
	rc := NewRouterClient(srv.URL)
	store := &markFailureStore{Store: driver, putErr: errStoreRefused}
	m := NewAckMark(rc, chanstore.New(store), p.add, p.remove)
	t.Cleanup(m.Stop)

	inbound(t, rc, "m1", "message")
	m.Typing(markJID, true)

	if len(p.targets()) != 0 {
		t.Errorf("an unrecorded mark stands: %v; calls: %s", p.targets(), p.seen())
	}
}

// The backstop of an earlier mark must not take a newer one down.
func TestAckMark_StaleBackstopLeavesTheNewerMark(t *testing.T) {
	st := testStore(t)
	p := newPlatform()
	m, rc := newMark(t, st, p)

	inbound(t, rc, "m1", "message")
	m.Typing(markJID, true)
	m.mu.Lock()
	old := m.timers[markJID].gen
	m.mu.Unlock()
	inbound(t, rc, "m2", "message")
	m.Typing(markJID, true)

	m.expire(markJID, old)

	if got := p.targets(); len(got) != 1 || got[0] != "m2" {
		t.Errorf("platform shows %v, want the newer mark m2 left", got)
	}
}

// With no off, the mark goes at the backstop.
func TestAckMark_BackstopRemovesALostOff(t *testing.T) {
	st := testStore(t)
	p := newPlatform()
	m, rc := newMark(t, st, p)
	m.ttl = 50 * time.Millisecond

	inbound(t, rc, "m1", "message")
	m.Typing(markJID, true)
	waitUntil(t, func() bool { return len(p.targets()) == 0 }, "the backstop never removed the mark")
}

// Stop ends the backstops and leaves the mark: a restart in the middle of a
// turn keeps its 👀.
func TestAckMark_StopKeepsTheMark(t *testing.T) {
	st := testStore(t)
	p := newPlatform()
	m, rc := newMark(t, st, p)

	inbound(t, rc, "m1", "message")
	m.Typing(markJID, true)
	m.Stop()

	if len(p.targets()) != 1 {
		t.Errorf("Stop removed the mark: calls %s", p.seen())
	}
	if _, held, _ := st.MarkFor(t.Context(), markJID); !held {
		t.Error("Stop dropped the record")
	}
}

// A message replayed from retention after a restart passes the same site as a
// live one, so its turn is marked too. The adapter restarted between the
// platform event and routd's recovery: the new helper has seen nothing live.
func TestAckMark_ReplayedInboundIsATrigger(t *testing.T) {
	st := testStore(t)
	stub, srv := newRoutdStub(503)
	defer srv.Close()
	rc := NewRouterClient(srv.URL)
	rc.RetainInbound(context.Background(), st)
	msg := InboundMsg{ID: "gh-c9", ChatJID: markJID, Verb: "mention"}
	if err := rc.SendMessage(msg); err == nil {
		t.Fatal("routd was down and the send reported success")
	}

	stub.status.Store(200)
	p := newPlatform()
	rc2 := NewRouterClient(srv.URL)
	rc2.RetainInbound(context.Background(), st)
	m := NewAckMark(rc2, st, p.add, p.remove)
	t.Cleanup(m.Stop)
	dueNow(t, st, msg.ID)
	rc2.SweepInboundOnce(context.Background())
	m.Typing(markJID, true)

	if want := "add gh-c9"; p.seen() != want {
		t.Errorf("calls = %q, want %q", p.seen(), want)
	}
}

func waitUntil(t *testing.T, ok func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal(msg)
}
