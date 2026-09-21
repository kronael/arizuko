package chanlib

import "testing"

// put is the write every body here makes, with the error raised rather than
// carried: a caller that cannot record the mapping is the failure these bodies
// are about, so a silent one would let them pass over it.
func put(t *testing.T, c *URLCache, url string) string {
	t.Helper()
	id, err := c.Put(t.Context(), url)
	if err != nil {
		t.Fatalf("put %s: %v", url, err)
	}
	return id
}

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
	id := put(t, before, url)

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
	id := put(t, c, evicted)
	put(t, c, "https://cdn.example.com/second.png") // pushes the first out of memory

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

	id := put(t, NewURLCache(10, st), url)
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
	id1 := put(t, c, "https://example.com/a.jpg")
	id2 := put(t, c, "https://example.com/a.jpg")
	if id1 != id2 {
		t.Errorf("same url returned different ids: %q vs %q", id1, id2)
	}
	if len(id1) != 12 {
		t.Errorf("id length = %d, want 12", len(id1))
	}
}

func TestURLCacheGetAfterPut(t *testing.T) {
	c := NewURLCache(10, nil)
	id := put(t, c, "https://example.com/x")
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
	a := put(t, c, "https://example.com/a")
	b := put(t, c, "https://example.com/b")
	put(t, c, "https://example.com/c") // evicts a (least recently used)
	if _, ok := get(t, c, a); ok {
		t.Error("a should have been evicted")
	}
	if _, ok := get(t, c, b); !ok {
		t.Error("b should still be present")
	}
}

func TestURLCacheLRUBumpsOnGet(t *testing.T) {
	c := NewURLCache(2, nil)
	a := put(t, c, "https://example.com/a")
	b := put(t, c, "https://example.com/b")
	// Access a -> becomes most recent; adding c should now evict b.
	if _, ok := get(t, c, a); !ok {
		t.Fatal("a missing")
	}
	put(t, c, "https://example.com/c")
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
