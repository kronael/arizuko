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
	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/objstore/dynstore/dyntest"
	"github.com/kronael/arizuko/retry"
)

type failingInboundStore struct {
	objstore.Store
	fail bool
}

var errInboundWrite = errors.New("inbound write failed")

type failOnceQuery struct {
	objstore.Store
	armed, failed atomic.Bool
}

func (s *failOnceQuery) Query(ctx context.Context, q objstore.Query) ([]objstore.Item, error) {
	if s.armed.Load() && !s.failed.Swap(true) {
		return nil, &retry.Error{Failure: retry.Failure{Kind: "permanent"}, Err: errors.New("store rejected query")}
	}
	return s.Store.Query(ctx, q)
}

// A permanent store error must not end redelivery for the process lifetime.
func TestSweepSurvivesPermanentStoreError(t *testing.T) {
	raw := dyntest.Store(t)
	for _, entity := range chanstore.Entities {
		if err := raw.CreateEntity(t.Context(), entity); err != nil {
			t.Fatal(err)
		}
	}
	fq := &failOnceQuery{Store: raw}
	st := chanstore.New(fq)
	msg := InboundMsg{ID: "x", ChatJID: "slack:c/1"}
	payload, err := json.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := st.Retain(t.Context(), msg.ID, string(payload), core.TS(now), retry.Decision{Failures: 1, NextAt: now.Add(-time.Hour)}, "retry"); err != nil {
		t.Fatal(err)
	}
	fq.armed.Store(true)
	old := inboundSweepEvery
	inboundSweepEvery = 20 * time.Millisecond
	defer func() { inboundSweepEvery = old }()
	stub, srv := newRoutdStub(http.StatusOK)
	defer srv.Close()
	rc := NewRouterClient(srv.URL)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	rc.RetainInbound(ctx, st)
	deadline := time.Now().Add(5 * time.Second)
	for len(stub.ids()) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if ids := stub.ids(); len(ids) != 1 || ids[0] != "x" {
		t.Fatalf("redelivered=%v", ids)
	}
}

func (s *failingInboundStore) GuardedBatch(ctx context.Context, checks []objstore.Check, writes []objstore.Write) (bool, error) {
	if s.fail {
		return false, errInboundWrite
	}
	return s.Store.GuardedBatch(ctx, checks, writes)
}

func TestSweepPropagatesTransitionErrors(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusServiceUnavailable} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			raw := dyntest.Store(t)
			for _, entity := range chanstore.Entities {
				if err := raw.CreateEntity(t.Context(), entity); err != nil {
					t.Fatal(err)
				}
			}
			failing := &failingInboundStore{Store: raw}
			st := chanstore.New(failing)
			msg := InboundMsg{ID: "x", ChatJID: "slack:c/1"}
			payload, err := json.Marshal(msg)
			if err != nil {
				t.Fatal(err)
			}
			now := time.Now()
			if err := st.Retain(t.Context(), msg.ID, string(payload), core.TS(now), retry.Decision{Failures: 1, NextAt: now.Add(-time.Hour)}, "retry"); err != nil {
				t.Fatal(err)
			}
			_, srv := newRoutdStub(status)
			defer srv.Close()
			rc := NewRouterClient(srv.URL)
			rc.pending = st
			failing.fail = true
			if err := rc.SweepInboundOnce(t.Context()); !errors.Is(err, errInboundWrite) {
				t.Fatalf("sweep err=%v", err)
			}
		})
	}
}

func TestInboundMalformedMetadataDoesNotPost(t *testing.T) {
	raw := dyntest.Store(t)
	for _, entity := range chanstore.Entities {
		if err := raw.CreateEntity(t.Context(), entity); err != nil {
			t.Fatal(err)
		}
	}
	_, err := raw.GuardedBatch(t.Context(), nil, []objstore.Write{{Op: objstore.Put, Entity: chanstore.InboundPending, Key: objstore.Key{Partition: "inbound", Sort: "x"}, Attrs: map[string]string{"attempts": "bad", "retry_state": "failed", "last_error": "stopped"}}})
	if err != nil {
		t.Fatal(err)
	}
	stub, srv := newRoutdStub(http.StatusOK)
	defer srv.Close()
	rc := NewRouterClient(srv.URL)
	rc.pending = chanstore.New(raw)
	err = rc.SendMessage(InboundMsg{ID: "x", ChatJID: "slack:c/1"})
	if !errors.Is(err, ErrInboundUnavailable) || len(stub.ids()) != 0 {
		t.Fatalf("err=%v posts=%v", err, stub.ids())
	}
}

func TestSweepStopsFailedPayloads(t *testing.T) {
	for _, tc := range []struct {
		name, payload   string
		failures, posts int
	}{
		{"exhausted", `{"id":"x","chat_jid":"slack:c/1"}`, 9, 1},
		{"malformed", `{`, 1, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := dyntest.Store(t)
			for _, entity := range chanstore.Entities {
				if err := raw.CreateEntity(t.Context(), entity); err != nil {
					t.Fatal(err)
				}
			}
			st := chanstore.New(raw)
			now := time.Now()
			if err := st.Retain(t.Context(), "x", tc.payload, core.TS(now),
				retry.Decision{Failures: tc.failures, NextAt: now.Add(-time.Minute)}, "retry"); err != nil {
				t.Fatal(err)
			}
			stub, srv := newRoutdStub(http.StatusServiceUnavailable)
			defer srv.Close()
			rc := NewRouterClient(srv.URL)
			rc.pending = st
			for range 2 {
				if err := rc.SweepInboundOnce(t.Context()); err != nil {
					t.Fatal(err)
				}
			}
			failure, err := st.ReadInboundFailure(t.Context(), "x")
			if err != nil || failure == nil || failure.Failure.Kind != "permanent" || len(stub.ids()) != tc.posts {
				t.Fatalf("failure=%v err=%v posts=%v", failure, err, stub.ids())
			}
			rows, err := raw.Query(t.Context(), objstore.Query{Entity: chanstore.InboundPending, Partition: "inbound"})
			if err != nil || len(rows) != 1 || rows[0].Attrs["payload"] != tc.payload {
				t.Fatalf("retained payload=%v err=%v", rows, err)
			}
			if count, err := st.HeldCount(t.Context()); err != nil || count != 1 {
				t.Fatalf("held count=%d err=%v", count, err)
			}
		})
	}
}

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
	rc.RetainInbound(t.Context(), st)

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

func TestRefusedInboundRemainsHeldWithoutRedelivery(t *testing.T) {
	_, srv := newRoutdStub(400)
	defer srv.Close()
	st := testStore(t)
	rc := NewRouterClient(srv.URL)
	rc.RetainInbound(t.Context(), st)

	err := rc.SendMessage(InboundMsg{ID: "x", ChatJID: "slack:c/1", Content: "hi"})
	if err == nil || errors.Is(err, ErrInboundLost) {
		t.Fatalf("permanent refusal must remain held: %v", err)
	}
	if n := pendingRows(t, st); n != 1 {
		t.Fatalf("held inbound = %d, want 1", n)
	}
	if due, err := st.DueForRedelivery(t.Context(), core.TS(time.Now().Add(24*time.Hour)), 0); err != nil || len(due) != 0 {
		t.Fatalf("permanent refusal due=%v err=%v", due, err)
	}
}

func TestRetainedInboundDuplicateDoesNotPost(t *testing.T) {
	for _, status := range []int{http.StatusBadRequest, http.StatusServiceUnavailable} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			stub, srv := newRoutdStub(status)
			defer srv.Close()
			st := testStore(t)
			rc := NewRouterClient(srv.URL)
			rc.RetainInbound(t.Context(), st)
			msg := InboundMsg{ID: "duplicate", ChatJID: "slack:c/1", Content: "hi"}
			first := rc.SendMessage(msg)
			if status == http.StatusServiceUnavailable {
				dueNow(t, st, msg.ID)
			}
			before := len(stub.ids())
			second := rc.SendMessage(msg)
			var failure *retry.Error
			if !errors.As(second, &failure) {
				t.Fatalf("duplicate error=%v, first=%v", second, first)
			}
			if (failure.Failure.Kind == "permanent") != (status == http.StatusBadRequest) {
				t.Fatalf("duplicate failure=%+v", failure)
			}
			if got := len(stub.ids()); got != before {
				t.Fatalf("duplicate posted: before=%d after=%d", before, got)
			}
		})
	}
}

func TestSweepRetainsARowRoutdRefuses(t *testing.T) {
	stub, srv := newRoutdStub(502)
	defer srv.Close()
	st := testStore(t)
	rc := NewRouterClient(srv.URL)
	rc.RetainInbound(t.Context(), st)

	_ = rc.SendMessage(InboundMsg{ID: "x", ChatJID: "slack:c/1", Content: "hi"})
	if n := pendingRows(t, st); n != 1 {
		t.Fatalf("retained inbound = %d, want 1", n)
	}
	stub.status.Store(400)
	dueNow(t, st, "x")
	rc.SweepInboundOnce(context.Background())
	if n := pendingRows(t, st); n != 1 {
		t.Fatalf("retained inbound = %d, want 1", n)
	}
}

// A still-failing sweep leaves the row and backs the next attempt off.
func TestSweepBacksOffAStillFailingRow(t *testing.T) {
	_, srv := newRoutdStub(503)
	defer srv.Close()
	st := testStore(t)
	rc := NewRouterClient(srv.URL)
	rc.RetainInbound(t.Context(), st)

	_ = rc.SendMessage(InboundMsg{ID: "x", ChatJID: "slack:c/1", Content: "hi"})
	dueNow(t, st, "x")
	rc.SweepInboundOnce(context.Background())

	got, ok := held(t, st, "x")
	if !ok {
		t.Fatal("the still-failing message was dropped instead of backed off")
	}
	if got.Attempts != 2 {
		t.Fatalf("attempts = %d, want 2", got.Attempts)
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
	rc.RetainInbound(t.Context(), st)

	msg := InboundMsg{ID: "x", ChatJID: "slack:c/1", Content: "hi"}
	_ = rc.SendMessage(msg)
	dueNow(t, st, "x")
	rc.SweepInboundOnce(context.Background())
	_ = rc.SendMessage(msg) // Slack redelivers the same event

	got, ok := held(t, st, "x")
	if !ok {
		t.Fatal("the redelivered message replaced the row instead of leaving it")
	}
	if got.Attempts != 2 {
		t.Fatalf("attempts = %d after a platform redelivery, want the sweep's 2", got.Attempts)
	}
	if n := pendingRows(t, st); n != 1 {
		t.Fatalf("retained inbound = %d, want 1", n)
	}
}

// dueNow makes one retained message due without changing its failure count.
func dueNow(t *testing.T, st *chanstore.Store, id string) {
	t.Helper()
	row, ok := held(t, st, id)
	if !ok {
		t.Fatalf("missing held inbound %s", id)
	}
	if err := st.Reschedule(t.Context(), row, retry.Decision{Failures: row.Attempts, NextAt: time.Now().Add(-time.Hour)},
		"forced due by a test"); err != nil {
		t.Fatalf("force due: %v", err)
	}
}

func TestInboundUnavailableDoesNotPost(t *testing.T) {
	stub, srv := newRoutdStub(503)
	defer srv.Close()
	st := testStore(t)
	rc := NewRouterClient(srv.URL)
	rc.RetainInbound(t.Context(), st)
	// The store goes away under the adapter, the way extenddb does.
	dropAdapterEntities("testadapter")

	err := rc.SendMessage(InboundMsg{ID: "slack:c/1/18", ChatJID: "slack:c/1", Content: "hello"})
	if !errors.Is(err, ErrInboundUnavailable) || len(stub.ids()) != 0 {
		t.Fatalf("SendMessage err = %v, posts = %v", err, stub.ids())
	}
}

// A 401 from routd (authd restarting, or routd without the registration) is
// the daemon being unavailable: the retained row stays due, not failed
// (BUGS L884).
func TestRetained401StaysDue(t *testing.T) {
	raw := dyntest.Store(t)
	for _, entity := range chanstore.Entities {
		if err := raw.CreateEntity(t.Context(), entity); err != nil {
			t.Fatal(err)
		}
	}
	st := chanstore.New(raw)
	_, srv := newRoutdStub(http.StatusUnauthorized)
	defer srv.Close()
	rc := NewRouterClient(srv.URL)
	rc.pending = st
	if err := rc.SendMessage(InboundMsg{ID: "x", ChatJID: "slack:c/1"}); !isAuthErr(err) {
		t.Fatalf("send err=%v, want the 401", err)
	}
	failure, err := st.ReadInboundFailure(t.Context(), "x")
	if err != nil || failure == nil || failure.Failure.Kind != "transient" {
		t.Fatalf("failure=%v err=%v, want a transient hold", failure, err)
	}
	due, err := st.DueForRedelivery(t.Context(), core.TS(time.Now().Add(24*time.Hour)), 10)
	if err != nil || len(due) != 1 || due[0].ID != "x" {
		t.Fatalf("due=%v err=%v", due, err)
	}
}
