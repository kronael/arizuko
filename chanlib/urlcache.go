package chanlib

import (
	"container/list"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"sync"

	"github.com/kronael/arizuko/chanlib/chanstore"
)

// URLCache maps the short opaque ids in an adapter's `/files/<id>` proxy
// paths back to the platform URLs; an id is a 12-char hex prefix of
// sha256(url), so the same URL always yields the same id. THE MAPPING IS THE
// FILE and lives in chanstore's `file_urls` (BUGS L163); the LRU is a cache in
// front of it that bounds memory, not what can be answered — a purely
// in-memory map lost an id on restart and on eviction, the same loss to the
// turn that needed the file.
type URLCache struct {
	mu    sync.Mutex
	m     map[string]*list.Element
	order *list.List
	cap   int
	// nil leaves the cache memory-only, for a test of the LRU itself.
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

// Put records url and returns the short opaque id standing for it. THE ID IS
// ANSWERED EVEN WHEN THE RECORD FAILS: the proxy resolves it from memory for
// as long as this process lives, so dropping the attachment over a failed
// record would turn a lost restart into a loss right now; routd's enrich
// renders the `unavailable=` notice if the mapping is later gone.
func (c *URLCache) Put(ctx context.Context, url string) string {
	sum := sha256.Sum256([]byte(url))
	id := hex.EncodeToString(sum[:6])
	c.remember(id, url)
	if c.st == nil {
		return id
	}
	if err := c.st.RecordFileURL(ctx, id, url); err != nil {
		slog.ErrorContext(ctx, "attachment url not recorded", "id", id, "url", url, "err", err)
	}
	return id
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
	// Warm the LRU so a second fetch does not pay for the store again.
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
