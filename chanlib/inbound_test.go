package chanlib

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kronael/arizuko/chanlib/chanstore"
	"github.com/kronael/arizuko/core"
)

// routdStub answers /v1/messages with whatever status the test dials in, and
// records every id it received.
type routdStub struct {
	status atomic.Int32
	got    atomic.Value // []string
}

func newRoutdStub(status int) (*routdStub, *httptest.Server) {
	s := &routdStub{}
	s.status.Store(int32(status))
	s.got.Store([]string{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var m InboundMsg
		_ = json.NewDecoder(r.Body).Decode(&m)
		s.got.Store(append(append([]string{}, s.got.Load().([]string)...), m.ID))
		code := int(s.status.Load())
		if code != 200 {
			w.WriteHeader(code)
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	return s, srv
}

func (s *routdStub) ids() []string { return s.got.Load().([]string) }

func pendingRows(t *testing.T, st *chanstore.Store) int {
	t.Helper()
	n, err := PendingInboundCount(t.Context(), st)
	if err != nil {
		t.Fatalf("count the retained inbound: %v", err)
	}
	return n
}

// held reads one retained message back through the sweep's own read, by asking
// for everything due far enough in the future that the backoff cannot hide it.
// That is how a body checks the attempt count without naming a table.
func held(t *testing.T, st *chanstore.Store, id string) (chanstore.Held, bool) {
	t.Helper()
	due, err := st.DueForRedelivery(t.Context(), core.TS(time.Now().Add(24*time.Hour)), 0)
	if err != nil {
		t.Fatalf("read the retained inbound: %v", err)
	}
	for _, d := range due {
		if d.ID == id {
			return d, true
		}
	}
	return chanstore.Held{}, false
}

// The L25 shape: routd is down, the adapter already told the platform 200, and
// the message must survive to be delivered when routd comes back.
func TestRetainedInboundIsDeliveredAfterRoutdRecovers(t *testing.T) {
	stub, srv := newRoutdStub(503)
	defer srv.Close()
	st := testStore(t)
	rc := NewRouterClient(srv.URL)
	rc.RetainInbound(context.Background(), st)

	msg := InboundMsg{ID: "slack:c/1/17", ChatJID: "slack:c/1", Content: "hello"}
	err := rc.SendMessage(msg)
	if err == nil {
		t.Fatal("SendMessage reported success while routd answered 503")
	}
	if errors.Is(err, ErrInboundLost) {
		t.Fatalf("a retained message was reported lost: %v", err)
	}
	if n := pendingRows(t, st); n != 1 {
		t.Fatalf("retained inbound = %d, want 1 — the message was dropped", n)
	}

	// routd comes back; the sweep re-delivers with the SAME id, which is what
	// makes routd's own create-if-absent a no-op rather than a duplicate.
	stub.status.Store(200)
	dueNow(t, st, msg.ID)
	rc.SweepInboundOnce(context.Background())

	if n := pendingRows(t, st); n != 0 {
		t.Fatalf("retained inbound = %d after recovery, want 0", n)
	}
	// Every post carried the SAME id (postInbound's own second attempt included),
	// which is what makes routd collapse them to one message.
	ids := stub.ids()
	if len(ids) < 2 {
		t.Fatalf("routd saw %d posts, want the live attempts plus the sweep's", len(ids))
	}
	for i, got := range ids {
		if got != msg.ID {
			t.Fatalf("post %d carried id %q, want %q", i, got, msg.ID)
		}
	}
}

// A payload routd REFUSED (400) is not retained: replaying it would fail
// identically forever. It must still surface as an error to the caller.
func TestRefusedInboundIsNotRetained(t *testing.T) {
	_, srv := newRoutdStub(400)
	defer srv.Close()
	st := testStore(t)
	rc := NewRouterClient(srv.URL)
	rc.RetainInbound(context.Background(), st)

	if err := rc.SendMessage(InboundMsg{ID: "x", ChatJID: "slack:c/1", Content: "hi"}); err == nil {
		t.Fatal("SendMessage reported success on a 400")
	}
	if n := pendingRows(t, st); n != 0 {
		t.Fatalf("retained inbound = %d, want 0 — a 400 must not be retried", n)
	}
}

// A retained row whose payload routd later refuses is dropped by the sweep
// instead of retried forever.
func TestSweepDropsARowRoutdRefuses(t *testing.T) {
	stub, srv := newRoutdStub(502)
	defer srv.Close()
	st := testStore(t)
	rc := NewRouterClient(srv.URL)
	rc.RetainInbound(context.Background(), st)

	_ = rc.SendMessage(InboundMsg{ID: "x", ChatJID: "slack:c/1", Content: "hi"})
	if n := pendingRows(t, st); n != 1 {
		t.Fatalf("retained inbound = %d, want 1", n)
	}
	stub.status.Store(400)
	dueNow(t, st, "x")
	rc.SweepInboundOnce(context.Background())
	if n := pendingRows(t, st); n != 0 {
		t.Fatalf("retained inbound = %d, want 0 — a refused message must be dropped", n)
	}
}

// A still-failing sweep leaves the row and backs the next attempt off.
func TestSweepBacksOffAStillFailingRow(t *testing.T) {
	_, srv := newRoutdStub(503)
	defer srv.Close()
	st := testStore(t)
	rc := NewRouterClient(srv.URL)
	rc.RetainInbound(context.Background(), st)

	_ = rc.SendMessage(InboundMsg{ID: "x", ChatJID: "slack:c/1", Content: "hi"})
	dueNow(t, st, "x")
	rc.SweepInboundOnce(context.Background())

	got, ok := held(t, st, "x")
	if !ok {
		t.Fatal("the still-failing message was dropped instead of backed off")
	}
	if got.Attempts != 1 {
		t.Fatalf("attempts = %d, want 1", got.Attempts)
	}
	// The backoff is checked through the sweep's own read: nothing is due 30s
	// from now, which is what "backed off beyond the base wait" means to the
	// only caller that asks.
	due, err := st.DueForRedelivery(t.Context(), core.TS(time.Now().Add(30*time.Second)), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 0 {
		t.Fatalf("%d messages are due 30s from now, want a backoff past that", len(due))
	}
}

// Without RetainInbound the client is unchanged: nothing is stored and the
// error still surfaces. The cursor-gated adapters rely on exactly this.
func TestSendMessageWithoutRetentionStoresNothing(t *testing.T) {
	_, srv := newRoutdStub(503)
	defer srv.Close()
	rc := NewRouterClient(srv.URL)
	if err := rc.SendMessage(InboundMsg{ID: "x", ChatJID: "slack:c/1", Content: "hi"}); err == nil {
		t.Fatal("SendMessage reported success while routd answered 503")
	}
	if st := rc.pendingStore(); st != nil {
		t.Fatal("pending store wired without RetainInbound")
	}
}

// A platform that re-delivers the same event must not reset the row's backoff.
func TestRetainKeepsTheFirstRowsBackoff(t *testing.T) {
	_, srv := newRoutdStub(503)
	defer srv.Close()
	st := testStore(t)
	rc := NewRouterClient(srv.URL)
	rc.RetainInbound(context.Background(), st)

	msg := InboundMsg{ID: "x", ChatJID: "slack:c/1", Content: "hi"}
	_ = rc.SendMessage(msg)
	dueNow(t, st, "x")
	rc.SweepInboundOnce(context.Background())
	_ = rc.SendMessage(msg) // Slack redelivers the same event

	got, ok := held(t, st, "x")
	if !ok {
		t.Fatal("the redelivered message replaced the row instead of leaving it")
	}
	if got.Attempts != 1 {
		t.Fatalf("attempts = %d after a platform redelivery, want the sweep's 1", got.Attempts)
	}
	if n := pendingRows(t, st); n != 1 {
		t.Fatalf("retained inbound = %d, want 1", n)
	}
}

func TestPermanentIngressClassification(t *testing.T) {
	for _, tc := range []struct {
		code int
		want bool
	}{{400, true}, {403, true}, {413, true}, {422, true},
		{401, false}, {429, false}, {500, false}, {502, false}, {503, false}} {
		if got := PermanentIngress(&StatusError{Path: "/v1/messages", Code: tc.code}); got != tc.want {
			t.Errorf("PermanentIngress(%d) = %v, want %v", tc.code, got, tc.want)
		}
	}
	if PermanentIngress(errDial) {
		t.Error("a transport error must not classify as permanent")
	}
}

var errDial = &StatusError{Path: "/v1/messages", Code: 0}

// dueNow makes one retained message due, so a test drives the sweep without
// sleeping through the backoff. It resets the attempt count to zero along the
// way, which every caller wants: each one forces a message the sweep has not
// touched yet.
func dueNow(t *testing.T, st *chanstore.Store, id string) {
	t.Helper()
	if err := st.Reschedule(t.Context(), id, 0, core.TS(time.Now().Add(-time.Hour)),
		"forced due by a test"); err != nil {
		t.Fatalf("force due: %v", err)
	}
}

// Neither routd nor the retention store took the message, which is ONE event:
// both ride the same store. SendMessage must say so, because an adapter whose
// HTTP response acknowledges the platform cannot otherwise tell this apart
// from a retained message and answers success for a message nothing holds
// (BUGS L415).
func TestInboundNeitherDeliveredNorRetainedIsReportedLost(t *testing.T) {
	_, srv := newRoutdStub(503)
	defer srv.Close()
	st := testStore(t)
	rc := NewRouterClient(srv.URL)
	rc.RetainInbound(context.Background(), st)
	// The store goes away under the adapter, the way extenddb does.
	dropAdapterEntities("testadapter")

	err := rc.SendMessage(InboundMsg{ID: "slack:c/1/18", ChatJID: "slack:c/1", Content: "hello"})
	if !errors.Is(err, ErrInboundLost) {
		t.Fatalf("SendMessage err = %v, want ErrInboundLost", err)
	}
}
