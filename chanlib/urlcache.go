package chanlib

import (
	"container/list"
	"crypto/sha256"
	"encoding/hex"
	"sync"
)

// URLCache is a bounded-size LRU cache mapping short opaque ids to URLs.
// Id is a 12-char hex prefix of sha256(url) — stable, collision-resistant
// enough for /files/<id> proxy endpoints in channel adapters.
type URLCache struct {
	mu    sync.Mutex
	m     map[string]*list.Element
	order *list.List
	cap   int
}

type urlEntry struct{ id, url string }

const DefaultURLCacheSize = 4096

func NewURLCache(cap int) *URLCache {
	if cap <= 0 {
		cap = DefaultURLCacheSize
	}
	return &URLCache{m: map[string]*list.Element{}, order: list.New(), cap: cap}
}

// Put stores url and returns a short opaque id. Idempotent: same url
// always returns the same id. Bumps LRU position.
func (c *URLCache) Put(url string) string {
	sum := sha256.Sum256([]byte(url))
	id := hex.EncodeToString(sum[:6])
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.m[id]; ok {
		c.order.MoveToFront(el)
		return id
	}
	el := c.order.PushFront(&urlEntry{id, url})
	c.m[id] = el
	for c.order.Len() > c.cap {
		back := c.order.Back()
		c.order.Remove(back)
		delete(c.m, back.Value.(*urlEntry).id)
	}
	return id
}

// Get returns the stored url and bumps LRU position.
func (c *URLCache) Get(id string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.m[id]
	if !ok {
		return "", false
	}
	c.order.MoveToFront(el)
	return el.Value.(*urlEntry).url, true
}
