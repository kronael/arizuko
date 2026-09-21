package chanlib

import (
	"container/list"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sync"

	"github.com/kronael/arizuko/chanlib/chanstore"
)

// URLCache maps the short opaque ids in an adapter's `/files/<id>` proxy paths
// back to the platform URLs they stand for. Id is a 12-char hex prefix of
// sha256(url) — stable, so the same URL always yields the same id.
//
// THE MAPPING IS THE FILE, and chanstore's `file_urls` is where it lives —
// that entity states why (BUGS L163). The LRU here is a cache in front of it:
// it bounds what is held in memory, not what can be answered, because a miss
// falls through to the store. A purely in-memory map lost an id two ways — a
// restart emptied it, and a busy adapter evicted the oldest while running —
// and both are the same loss to the turn that needed the file.
type URLCache struct {
	mu    sync.Mutex
	m     map[string]*list.Element
	order *list.List
	cap   int
	// st is where the mapping actually lives. nil leaves the cache
	// memory-only, which is what a test that exercises the LRU itself wants;
	// every adapter passes its own store.
	st *chanstore.Store
}

type urlEntry struct{ id, url string }

const DefaultURLCacheSize = 4096

// NewURLCache builds a cache over st, which is an argument so that no cache
// can serve an id before it has its record. cap <= 0 takes the default.
func NewURLCache(cap int, st *chanstore.Store) *URLCache {
	if cap <= 0 {
		cap = DefaultURLCacheSize
	}
	return &URLCache{m: map[string]*list.Element{}, order: list.New(), cap: cap, st: st}
}

// Put records url and returns the short opaque id standing for it. Idempotent:
// the same url always returns the same id.
//
// THE ID IS RETURNED EVEN WHEN THE RECORD FAILS, together with the error. The
// caller is building an inbound envelope: the proxy resolves the id from memory
// for as long as this process lives, so dropping the attachment over a failed
// record would turn a lost restart into a loss right now. The caller logs and
// carries on; routd's enrich renders the `unavailable=` notice if the mapping
// is later gone, so the user is told either way.
func (c *URLCache) Put(ctx context.Context, url string) (string, error) {
	sum := sha256.Sum256([]byte(url))
	id := hex.EncodeToString(sum[:6])
	c.remember(id, url)
	if c.st == nil {
		return id, nil
	}
	return id, c.st.RecordFileURL(ctx, id, url)
}

// Get returns the url an id stands for. A store miss is reported as not-found,
// not as an error: an id nothing recorded is the proxy's 404.
func (c *URLCache) Get(ctx context.Context, id string) (string, bool, error) {
	if url, ok := c.lookup(id); ok {
		return url, true, nil
	}
	if c.st == nil {
		return "", false, nil
	}
	url, held, err := c.st.FileURL(ctx, id)
	if err != nil || !held {
		return "", false, err
	}
	// Warm the LRU so a second fetch of the same file — a retried download, a
	// re-fed turn — does not pay for the store again.
	c.remember(id, url)
	return url, true, nil
}

// remember inserts id under the LRU bound, bumping it if already held.
func (c *URLCache) remember(id, url string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.m[id]; ok {
		c.order.MoveToFront(el)
		return
	}
	el := c.order.PushFront(&urlEntry{id, url})
	c.m[id] = el
	for c.order.Len() > c.cap {
		back := c.order.Back()
		c.order.Remove(back)
		delete(c.m, back.Value.(*urlEntry).id)
	}
}

// lookup answers from memory alone and bumps LRU position.
func (c *URLCache) lookup(id string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.m[id]
	if !ok {
		return "", false
	}
	c.order.MoveToFront(el)
	return el.Value.(*urlEntry).url, true
}
