package chanlib

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
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

func pendingRows(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM inbound_pending`).Scan(&n); err != nil {
		t.Fatalf("count inbound_pending: %v", err)
	}
	return n
}

// The L25 shape: routd is down, the adapter already told the platform 200, and
// the message must survive to be delivered when routd comes back.
func TestRetainedInboundIsDeliveredAfterRoutdRecovers(t *testing.T) {
	stub, srv := newRoutdStub(503)
	defer srv.Close()
	db := testDedupDB(t)
	rc := NewRouterClient(srv.URL)
	rc.RetainInbound(context.Background(), db)

	msg := InboundMsg{ID: "slack:c/1/17", ChatJID: "slack:c/1", Content: "hello"}
	if err := rc.SendMessage(msg); err == nil {
		t.Fatal("SendMessage reported success while routd answered 503")
	}
	if n := pendingRows(t, db); n != 1 {
		t.Fatalf("inbound_pending rows = %d, want 1 — the message was dropped", n)
	}
	if n, err := PendingInboundCount(db); err != nil || n != 1 {
		t.Fatalf("PendingInboundCount() = %d, %v; want 1, nil", n, err)
	}

	// routd comes back; the sweep re-delivers with the SAME id, which is what
	// makes routd's INSERT OR IGNORE a no-op rather than a duplicate.
	stub.status.Store(200)
	dueNow(t, db)
	rc.SweepInboundOnce(context.Background())

	if n := pendingRows(t, db); n != 0 {
		t.Fatalf("inbound_pending rows = %d after recovery, want 0", n)
	}
	// Every post carried the SAME id (postInbound's own second attempt included),
	// which is what makes routd's INSERT OR IGNORE collapse them to one row.
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
	db := testDedupDB(t)
	rc := NewRouterClient(srv.URL)
	rc.RetainInbound(context.Background(), db)

	if err := rc.SendMessage(InboundMsg{ID: "x", ChatJID: "slack:c/1", Content: "hi"}); err == nil {
		t.Fatal("SendMessage reported success on a 400")
	}
	if n := pendingRows(t, db); n != 0 {
		t.Fatalf("inbound_pending rows = %d, want 0 — a 400 must not be retried", n)
	}
}

// A retained row whose payload routd later refuses is dropped by the sweep
// instead of retried forever.
func TestSweepDropsARowRoutdRefuses(t *testing.T) {
	stub, srv := newRoutdStub(502)
	defer srv.Close()
	db := testDedupDB(t)
	rc := NewRouterClient(srv.URL)
	rc.RetainInbound(context.Background(), db)

	_ = rc.SendMessage(InboundMsg{ID: "x", ChatJID: "slack:c/1", Content: "hi"})
	if n := pendingRows(t, db); n != 1 {
		t.Fatalf("inbound_pending rows = %d, want 1", n)
	}
	stub.status.Store(400)
	dueNow(t, db)
	rc.SweepInboundOnce(context.Background())
	if n := pendingRows(t, db); n != 0 {
		t.Fatalf("inbound_pending rows = %d, want 0 — a refused row must be dropped", n)
	}
}

// A still-failing sweep leaves the row and backs the next attempt off.
func TestSweepBacksOffAStillFailingRow(t *testing.T) {
	_, srv := newRoutdStub(503)
	defer srv.Close()
	db := testDedupDB(t)
	rc := NewRouterClient(srv.URL)
	rc.RetainInbound(context.Background(), db)

	_ = rc.SendMessage(InboundMsg{ID: "x", ChatJID: "slack:c/1", Content: "hi"})
	dueNow(t, db)
	rc.SweepInboundOnce(context.Background())

	var attempts int
	var next string
	if err := db.QueryRow(
		`SELECT attempts, next_attempt_at FROM inbound_pending WHERE id='x'`).Scan(&attempts, &next); err != nil {
		t.Fatalf("read row: %v", err)
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1", attempts)
	}
	at, err := time.Parse(time.RFC3339Nano, next)
	if err != nil {
		t.Fatalf("parse next_attempt_at %q: %v", next, err)
	}
	if !at.After(time.Now().Add(30 * time.Second)) {
		t.Fatalf("next_attempt_at = %s, want a backoff beyond 30s", next)
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
	if db := rc.pendingDB(); db != nil {
		t.Fatal("pending store wired without RetainInbound")
	}
}

// A platform that re-delivers the same event must not reset the row's backoff.
func TestRetainKeepsTheFirstRowsBackoff(t *testing.T) {
	_, srv := newRoutdStub(503)
	defer srv.Close()
	db := testDedupDB(t)
	rc := NewRouterClient(srv.URL)
	rc.RetainInbound(context.Background(), db)

	msg := InboundMsg{ID: "x", ChatJID: "slack:c/1", Content: "hi"}
	_ = rc.SendMessage(msg)
	dueNow(t, db)
	rc.SweepInboundOnce(context.Background())
	_ = rc.SendMessage(msg) // Slack redelivers the same event

	var attempts int
	if err := db.QueryRow(`SELECT attempts FROM inbound_pending WHERE id='x'`).Scan(&attempts); err != nil {
		t.Fatalf("read row: %v", err)
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d after a platform redelivery, want the sweep's 1", attempts)
	}
	if n := pendingRows(t, db); n != 1 {
		t.Fatalf("inbound_pending rows = %d, want 1", n)
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

// dueNow makes every retained row due, so a test drives the sweep without
// sleeping through the backoff.
func dueNow(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(
		`UPDATE inbound_pending SET next_attempt_at=?`,
		time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("force due: %v", err)
	}
}
