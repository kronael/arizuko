package chanlib

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/kronael/arizuko/chanlib/chanstore"
	"github.com/kronael/arizuko/objstore/dynstore"
)

// testStore opens a real adapter store and hands back the one chanlib's own
// code is given, so these bodies exercise the opener too. The adapter's tables
// are named for its SERVICE, not for this test, so the body drops them when it
// ends: two bodies here use the same keys, and a leftover row would be the next
// one's fixture.
func testStore(t *testing.T) *chanstore.Store {
	t.Helper()
	as, err := OpenAdapterStore("testadapter")
	if err != nil {
		t.Fatalf("open the adapter store: %v", err)
	}
	t.Cleanup(func() {
		dropAdapterEntities("testadapter")
		as.Close() //nolint:errcheck // the test is ending
	})
	return as.Chan
}

// dropAdapterEntities removes what the opener made, so nothing this body wrote
// outlives it — here or on the endpoint after the run.
func dropAdapterEntities(service string) {
	h, err := dynstore.FromEnv(dynstore.Prefix(service))
	if err != nil {
		return
	}
	ctx := context.Background()
	left, err := h.Entities(ctx)
	if err != nil {
		return
	}
	for _, e := range left {
		_ = h.DropEntity(ctx, e)
	}
}

// counter records how many times the platform was actually called.
type counter struct {
	n   int
	id  string
	err error
}

func (c *counter) post() (string, error) {
	c.n++
	return c.id, c.err
}

// A redelivery of a settled key returns the FIRST platform id and makes no
// second platform call. This is the whole point of D5: routd re-sends a row
// that stays `pending`, so the same key arrives twice by design.
func TestDeliverOnceReturnsTheRecordedIDWithoutPostingAgain(t *testing.T) {
	st := testStore(t)
	c := &counter{id: "plat-1"}

	id, err := deliverOnce(st, "row-1", "slack:c/1", c.post)
	if err != nil || id != "plat-1" {
		t.Fatalf("first delivery = (%q, %v), want (plat-1, nil)", id, err)
	}
	id, err = deliverOnce(st, "row-1", "slack:c/1", c.post)
	if err != nil || id != "plat-1" {
		t.Fatalf("redelivery = (%q, %v), want (plat-1, nil)", id, err)
	}
	if c.n != 1 {
		t.Fatalf("platform called %d times, want 1", c.n)
	}
}

// An attempt that never settled is refused, not repeated. The adapter cannot
// tell a lost response from a lost request, so it reports the ambiguity and
// routd dead-letters the row.
func TestDeliverOnceRefusesAnUnsettledRedelivery(t *testing.T) {
	st := testStore(t)
	boom := &counter{err: errors.New("connection reset")}

	if _, err := deliverOnce(st, "row-2", "slack:c/1", boom.post); err == nil {
		t.Fatal("first delivery must surface the platform error")
	}
	next := &counter{id: "plat-2"}
	_, err := deliverOnce(st, "row-2", "slack:c/1", next.post)
	if !errors.Is(err, ErrDeliveryUnresolved) {
		t.Fatalf("redelivery err = %v, want ErrDeliveryUnresolved", err)
	}
	if next.n != 0 {
		t.Fatalf("platform called %d times after an unresolved claim, want 0", next.n)
	}
}

// ErrPermanent is the one failure that proves nothing was posted, so it frees
// the key. Holding it would dead-letter a message the operator can still fix.
func TestDeliverOncePermanentFailureFreesTheKey(t *testing.T) {
	st := testStore(t)
	refused := &counter{err: fmt.Errorf("bad channel: %w", ErrPermanent)}

	if _, err := deliverOnce(st, "row-3", "slack:c/1", refused.post); err == nil {
		t.Fatal("permanent failure must surface")
	}
	next := &counter{id: "plat-3"}
	id, err := deliverOnce(st, "row-3", "slack:c/1", next.post)
	if err != nil || id != "plat-3" {
		t.Fatalf("retry after a permanent failure = (%q, %v), want (plat-3, nil)", id, err)
	}
	if next.n != 1 {
		t.Fatalf("platform called %d times, want 1", next.n)
	}
}

// ErrNotDelivered is a transient refusal that PROVES nothing was posted (a
// 429, an `ok:false`), so it frees the key: routd's sweep must be able to
// retry on a fresh claim. Holding it turned every adapter-side rate limit
// into a guaranteed dead-letter.
func TestDeliverOnceNotDeliveredFreesTheKey(t *testing.T) {
	st := testStore(t)
	limited := &counter{err: fmt.Errorf("slack send: status 429: %w", ErrNotDelivered)}

	if _, err := deliverOnce(st, "row-5", "slack:c/1", limited.post); err == nil {
		t.Fatal("not-delivered failure must surface")
	}
	next := &counter{id: "plat-5"}
	id, err := deliverOnce(st, "row-5", "slack:c/1", next.post)
	if err != nil || id != "plat-5" {
		t.Fatalf("retry after a not-delivered failure = (%q, %v), want (plat-5, nil)", id, err)
	}
	if next.n != 1 {
		t.Fatalf("platform called %d times, want 1", next.n)
	}
}

// No store and no key both mean "no dedup": a local-dev adapter with no state
// mount, and a send routd issued without a key, must still deliver every time.
func TestDeliverOnceWithoutDedupAlwaysPosts(t *testing.T) {
	for _, c := range []struct {
		name string
		st   *chanstore.Store
		key  string
	}{
		{"no store", nil, "row-4"},
		{"no key", testStore(t), ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := &counter{id: "plat-4"}
			for i := range 2 {
				if _, err := deliverOnce(c.st, c.key, "slack:c/1", p.post); err != nil {
					t.Fatalf("delivery %d: %v", i, err)
				}
			}
			if p.n != 2 {
				t.Fatalf("platform called %d times, want 2", p.n)
			}
		})
	}
}
