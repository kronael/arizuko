package main

import "sync"

// sentIDs is a bounded FIFO set of recent message IDs the bot has sent.
// Used to recognise replies/reactions targeting the bot's own messages
// when the gateway payload only carries the referenced message ID and
// not the full referenced-message object (the user-mode Discord case).
type sentIDs struct {
	mu    sync.Mutex
	cap   int
	ids   map[string]struct{}
	order []string
}

func newSentIDs(cap int) *sentIDs {
	if cap <= 0 {
		cap = 256
	}
	return &sentIDs{cap: cap, ids: make(map[string]struct{}, cap)}
}

func (s *sentIDs) add(id string) {
	if s == nil || id == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.ids[id]; ok {
		return
	}
	if len(s.order) >= s.cap {
		drop := s.order[0]
		s.order = s.order[1:]
		delete(s.ids, drop)
	}
	s.ids[id] = struct{}{}
	s.order = append(s.order, id)
}

func (s *sentIDs) has(id string) bool {
	if s == nil || id == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.ids[id]
	return ok
}
