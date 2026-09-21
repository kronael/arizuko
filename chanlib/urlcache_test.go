package chanlib

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/kronael/arizuko/chanlib/chanstore"
	"github.com/kronael/arizuko/objstore"
)

// get answers from whichever tier holds the id, raising a store failure.
func get(t *testing.T, c *URLCache, id string) (string, bool) {
	t.Helper()
	url, ok, err := c.Get(t.Context(), id)
	if err != nil {
		t.Fatalf("get %s: %v", id, err)
	}
	return url, ok
}

// --- the durable mapping ---

// THE BODY THIS TYPE EXISTS FOR. The adapter rewrites an attachment's URL to
// `/files/<id>` before routd sees it, and routd's enrich downloads through that
// proxy when the turn runs — which can be after the adapter restarted. An
// in-memory map answered nothing then, so the agent got the text and no file
// (BUGS L163).
//
// The restart is a SECOND cache over the same store, because that is what a
// restart is: the process's memory is gone and the store is not.
func TestURLCacheResolvesAfterARestart(t *testing.T) {
	st := testStore(t)
	const url = "https://cdn.example.com/restart.png"

	before := NewURLCache(10, st)
	id := before.Put(t.Context(), url)

	after := NewURLCache(10, st)
	got, ok := get(t, after, id)
	if !ok {
		t.Fatalf("id %q did not resolve after the restart, so the attachment is lost", id)
	}
	if got != url {
		t.Errorf("url = %q, want %q", got, url)
	}
}

// The same loss without a restart: a busy adapter evicts the oldest mapping
// while it is running, and the id an envelope already carries stops resolving.
// The LRU bounds what is held in memory, not what can be answered.
func TestURLCacheResolvesAfterEviction(t *testing.T) {
	st := testStore(t)
	const evicted = "https://cdn.example.com/first.png"

	c := NewURLCache(1, st)
	id := c.Put(t.Context(), evicted)
	c.Put(t.Context(), "https://cdn.example.com/second.png") // pushes the first out of memory

	if _, held := c.lookup(id); held {
		t.Fatal("the first id is still in memory, so this body is not testing the fall-through")
	}
	got, ok := get(t, c, id)
	if !ok {
		t.Fatalf("id %q did not resolve after eviction", id)
	}
	if got != evicted {
		t.Errorf("url = %q, want %q", got, evicted)
	}
}

// A store hit warms the LRU, so a retried download does not pay for the store
// on every attempt.
func TestURLCacheGetWarmsMemory(t *testing.T) {
	st := testStore(t)
	const url = "https://cdn.example.com/warm.png"

	id := NewURLCache(10, st).Put(t.Context(), url)
	c := NewURLCache(10, st)
	if _, held := c.lookup(id); held {
		t.Fatal("a fresh cache already holds the id")
	}
	if _, ok := get(t, c, id); !ok {
		t.Fatal("the store did not answer")
	}
	if _, held := c.lookup(id); !held {
		t.Error("the store hit did not warm memory")
	}
}

// An id nothing recorded is a miss, not a failure: the proxy turns it into the
// 404 that is already its contract.
func TestURLCacheUnknownIDIsAMiss(t *testing.T) {
	c := NewURLCache(10, testStore(t))
	if _, ok := get(t, c, "deadbeefcafe"); ok {
		t.Error("an unrecorded id resolved")
	}
}

// --- the memory tier ---
//
// A nil store leaves the cache memory-only, which is what these bodies want:
// they are about the LRU itself.

func TestURLCachePutIdempotent(t *testing.T) {
	c := NewURLCache(10, nil)
	id1 := c.Put(t.Context(), "https://example.com/a.jpg")
	id2 := c.Put(t.Context(), "https://example.com/a.jpg")
	if id1 != id2 {
		t.Errorf("same url returned different ids: %q vs %q", id1, id2)
	}
	if len(id1) != 12 {
		t.Errorf("id length = %d, want 12", len(id1))
	}
}

func TestURLCacheGetAfterPut(t *testing.T) {
	c := NewURLCache(10, nil)
	id := c.Put(t.Context(), "https://example.com/x")
	u, ok := get(t, c, id)
	if !ok || u != "https://example.com/x" {
		t.Errorf("Get(%q) = %q, ok=%v", id, u, ok)
	}
	if _, ok := get(t, c, "missing"); ok {
		t.Error("expected not found")
	}
}

func TestURLCacheLRUEviction(t *testing.T) {
	c := NewURLCache(2, nil)
	a := c.Put(t.Context(), "https://example.com/a")
	b := c.Put(t.Context(), "https://example.com/b")
	c.Put(t.Context(), "https://example.com/c") // evicts a (least recently used)
	if _, ok := get(t, c, a); ok {
		t.Error("a should have been evicted")
	}
	if _, ok := get(t, c, b); !ok {
		t.Error("b should still be present")
	}
}

func TestURLCacheLRUBumpsOnGet(t *testing.T) {
	c := NewURLCache(2, nil)
	a := c.Put(t.Context(), "https://example.com/a")
	b := c.Put(t.Context(), "https://example.com/b")
	// Access a -> becomes most recent; adding c should now evict b.
	if _, ok := get(t, c, a); !ok {
		t.Fatal("a missing")
	}
	c.Put(t.Context(), "https://example.com/c")
	if _, ok := get(t, c, b); ok {
		t.Error("b should have been evicted")
	}
	if _, ok := get(t, c, a); !ok {
		t.Error("a should still be present")
	}
}

func TestURLCacheDefaultCap(t *testing.T) {
	c := NewURLCache(0, nil)
	if c.cap != DefaultURLCacheSize {
		t.Errorf("cap = %d, want %d", c.cap, DefaultURLCacheSize)
	}
}

// A store that refuses the write must not take the attachment with it. The id
// answers from memory for the life of this process, so Put returns it and says
// loudly what was lost — the alternative is three adapters each deciding the
// same thing, which is what this replaces.
func TestURLCachePutIsLoudAndStillAnswers(t *testing.T) {
	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelError})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	const url = "https://cdn.example.com/lost.png"
	c := NewURLCache(10, chanstore.New(refusingStore{}))
	id := c.Put(t.Context(), url)

	if id == "" {
		t.Fatal("a failed record took the id with it; the attachment is lost now, not on restart")
	}
	if got, held := c.lookup(id); !held || got != url {
		t.Errorf("memory does not hold the mapping: (%q, %v)", got, held)
	}
	if !strings.Contains(logs.String(), "attachment url not recorded") {
		t.Errorf("the failed record was silent: %q", logs.String())
	}
}

// refusingStore fails every write and answers no read.
type refusingStore struct{}

func (refusingStore) Query(context.Context, objstore.Query) ([]objstore.Item, error) {
	return nil, errStoreRefused
}

func (refusingStore) GuardedBatch(context.Context, []objstore.Check, []objstore.Write) (bool, error) {
	return false, errStoreRefused
}

var errStoreRefused = errors.New("the store refused")
