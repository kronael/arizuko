// Package admin holds the per-source-IP allowlist registry and its HTTP API.
// The registry is a simple in-memory map mutated via Register/Unregister.
// When constructed via NewPersistentRegistry, every mutation rewrites a
// JSON file atomically so the registry survives restarts.
package admin

import (
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
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

// Registry maps source-IP → Entry. Concurrent-safe. Optional disk
// persistence: when path is non-empty, every Set/Remove rewrites the
// file atomically (tmp + rename).
type Registry struct {
	mu   sync.RWMutex
	ips  map[string]Entry
	path string
}

func NewRegistry() *Registry {
	return &Registry{ips: map[string]Entry{}}
}

// NewPersistentRegistry returns a Registry backed by a JSON file at path.
// On startup the file is read if present; corrupt or missing files yield
// an empty registry (logged as a warning) so a stale snapshot can never
// keep the daemon from booting.
func NewPersistentRegistry(path string) (*Registry, error) {
	r := &Registry{ips: map[string]Entry{}, path: path}
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return r, nil
		}
		return nil, err
	}
	var wire []WireEntry
	if err := json.Unmarshal(b, &wire); err != nil {
		slog.Warn("registry: corrupt state file, starting empty", "path", path, "err", err)
		return r, nil
	}
	for _, e := range wire {
		if e.IP == "" {
			continue
		}
		cp := make([]string, len(e.Allowlist))
		copy(cp, e.Allowlist)
		r.ips[e.IP] = Entry{ID: e.ID, Allowlist: cp}
	}
	return r, nil
}

// flushLocked persists the in-memory map to disk. Caller must hold r.mu.
// Best-effort: failure is logged but does not roll back the in-memory
// mutation — the proxy must not refuse to register agents because the
// disk is full.
func (r *Registry) flushLocked() {
	if r.path == "" {
		return
	}
	out := make([]WireEntry, 0, len(r.ips))
	for ip, e := range r.ips {
		cp := make([]string, len(e.Allowlist))
		copy(cp, e.Allowlist)
		out = append(out, WireEntry{IP: ip, ID: e.ID, Allowlist: cp})
	}
	data, err := json.Marshal(out)
	if err != nil {
		slog.Warn("registry: marshal", "err", err)
		return
	}
	dir := filepath.Dir(r.path)
	tmp, err := os.CreateTemp(dir, ".registry-*.json")
	if err != nil {
		slog.Warn("registry: tmp create", "dir", dir, "err", err)
		return
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		slog.Warn("registry: tmp write", "err", err)
		return
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		slog.Warn("registry: tmp close", "err", err)
		return
	}
	if err := os.Rename(tmpPath, r.path); err != nil {
		os.Remove(tmpPath)
		slog.Warn("registry: rename", "err", err)
	}
}

func (r *Registry) Set(ip, id string, list []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := make([]string, len(list))
	copy(cp, list)
	r.ips[ip] = Entry{ID: id, Allowlist: cp}
	r.flushLocked()
}

func (r *Registry) Remove(ip string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.ips, ip)
	r.flushLocked()
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
