package chanreg

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"
)

type Entry struct {
	Name         string
	URL          string
	JIDPrefixes  []string
	Capabilities map[string]bool
	Token        string
	HealthFails  int
	RegisteredAt time.Time
}

func (e *Entry) HasCap(cap string) bool { return e.Capabilities[cap] }

type Registry struct {
	mu      sync.RWMutex
	entries map[string]*Entry // keyed by name
	byToken map[string]*Entry // keyed by session token
	secret string
}

func New(secret string) *Registry {
	return &Registry{
		entries: make(map[string]*Entry),
		byToken: make(map[string]*Entry),
		secret:  secret,
	}
}

func (r *Registry) Register(name, url string, prefixes []string, caps map[string]bool) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if old, ok := r.entries[name]; ok {
		delete(r.byToken, old.Token)
	}

	token, err := genToken()
	if err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}

	e := &Entry{
		Name:         name,
		URL:          url,
		JIDPrefixes:  prefixes,
		Capabilities: caps,
		Token:        token,
		RegisteredAt: time.Now(),
	}
	r.entries[name] = e
	r.byToken[token] = e

	return token, nil
}

func (r *Registry) Deregister(name string) {
	r.mu.Lock()
	e, ok := r.entries[name]
	if ok {
		delete(r.byToken, e.Token)
		delete(r.entries, name)
	}
	r.mu.Unlock()
}

func (r *Registry) Get(name string) *Entry {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.entries[name]
}

func (r *Registry) ByToken(token string) *Entry {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.byToken[token]
}

func (r *Registry) All() map[string]*Entry {
	r.mu.RLock()
	defer r.mu.RUnlock()
	cp := make(map[string]*Entry, len(r.entries))
	for k, v := range r.entries {
		cp[k] = v
	}
	return cp
}

func (r *Registry) Secret() string { return r.secret }

// ForJID returns the first registered channel whose JID prefix matches jid.
func (r *Registry) ForJID(jid string) *Entry {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, e := range r.entries {
		for _, p := range e.JIDPrefixes {
			if strings.HasPrefix(jid, p) {
				return e
			}
		}
	}
	return nil
}

func (r *Registry) RecordHealthFail(name string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.entries[name]
	if !ok {
		return 0
	}
	e.HealthFails++
	return e.HealthFails
}

func (r *Registry) ResetHealth(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if e, ok := r.entries[name]; ok {
		e.HealthFails = 0
	}
}

func genToken() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
