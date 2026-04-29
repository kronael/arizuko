// Package admin holds the per-source-IP allowlist registry and its HTTP API.
// The registry is a simple in-memory map mutated via Register/Unregister.
package admin

import (
	"sync"

	"github.com/onvos/arizuko/crackbox/pkg/match"
)

// Entry is one (id, allowlist) tuple. id is opaque — the proxy treats it
// purely as a label for logs; arizuko sets it to the agent's folder, the
// crackbox-run wrapper sets it to a generated name, etc.
type Entry struct {
	ID        string
	Allowlist []string
}

// Registry maps source-IP → Entry. Concurrent-safe.
type Registry struct {
	mu  sync.RWMutex
	ips map[string]Entry
}

func NewRegistry() *Registry {
	return &Registry{ips: map[string]Entry{}}
}

func (r *Registry) Set(ip, id string, list []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := make([]string, len(list))
	copy(cp, list)
	r.ips[ip] = Entry{ID: id, Allowlist: cp}
}

func (r *Registry) Remove(ip string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.ips, ip)
}

func (r *Registry) Lookup(ip string) (id string, list []string, ok bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, ok := r.ips[ip]
	if !ok {
		return "", nil, false
	}
	return e.ID, e.Allowlist, true
}

// Allow returns the id for the source IP and true iff host matches the
// registered allowlist for that IP.
func (r *Registry) Allow(ip, host string) (id string, ok bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, found := r.ips[ip]
	if !found {
		return "", false
	}
	return e.ID, match.Host(e.Allowlist, host)
}

func (r *Registry) Snapshot() map[string]Entry {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[string]Entry, len(r.ips))
	for k, v := range r.ips {
		cp := make([]string, len(v.Allowlist))
		copy(cp, v.Allowlist)
		out[k] = Entry{ID: v.ID, Allowlist: cp}
	}
	return out
}
