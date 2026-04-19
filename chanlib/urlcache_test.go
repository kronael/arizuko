package chanlib

import "testing"

func TestURLCachePutIdempotent(t *testing.T) {
	c := NewURLCache(10)
	id1 := c.Put("https://example.com/a.jpg")
	id2 := c.Put("https://example.com/a.jpg")
	if id1 != id2 {
		t.Errorf("same url returned different ids: %q vs %q", id1, id2)
	}
	if len(id1) != 12 {
		t.Errorf("id length = %d, want 12", len(id1))
	}
}

func TestURLCacheGetAfterPut(t *testing.T) {
	c := NewURLCache(10)
	id := c.Put("https://example.com/x")
	u, ok := c.Get(id)
	if !ok || u != "https://example.com/x" {
		t.Errorf("Get(%q) = %q, ok=%v", id, u, ok)
	}
	if _, ok := c.Get("missing"); ok {
		t.Error("expected not found")
	}
}

func TestURLCacheLRUEviction(t *testing.T) {
	c := NewURLCache(2)
	a := c.Put("https://example.com/a")
	b := c.Put("https://example.com/b")
	c.Put("https://example.com/c") // evicts a (least recently used)
	if _, ok := c.Get(a); ok {
		t.Error("a should have been evicted")
	}
	if _, ok := c.Get(b); !ok {
		t.Error("b should still be present")
	}
}

func TestURLCacheLRUBumpsOnGet(t *testing.T) {
	c := NewURLCache(2)
	a := c.Put("https://example.com/a")
	b := c.Put("https://example.com/b")
	// Access a -> becomes most recent; adding c should now evict b.
	if _, ok := c.Get(a); !ok {
		t.Fatal("a missing")
	}
	c.Put("https://example.com/c")
	if _, ok := c.Get(b); ok {
		t.Error("b should have been evicted")
	}
	if _, ok := c.Get(a); !ok {
		t.Error("a should still be present")
	}
}

func TestURLCacheDefaultCap(t *testing.T) {
	c := NewURLCache(0)
	if c.cap != DefaultURLCacheSize {
		t.Errorf("cap = %d, want %d", c.cap, DefaultURLCacheSize)
	}
}
