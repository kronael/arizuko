package core

import (
	"regexp"
	"testing"
)

// ant/src/index.ts refuses to `claude --resume` any session id that is not a
// UUID and starts the turn fresh instead. This regexp is that guard, copied.
var antResumableRE = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// The id NewSessionID mints names the child topic's Claude Code transcript, so
// the agent must be able to resume it. A "sess-<nano>" placeholder passed the
// old tests and lost every forked topic's context in production: routd copied
// the parent transcript to a filename the agent then refused to open, and the
// first turn of every new thread answered with no history at all.
func TestNewSessionIDIsResumable(t *testing.T) {
	for range 8 {
		id := NewSessionID()
		if !antResumableRE.MatchString(id) {
			t.Fatalf("NewSessionID() = %q; the agent will not resume it, so the topic starts blind", id)
		}
	}
}

func TestNewSessionIDIsUnique(t *testing.T) {
	seen := map[string]bool{}
	for range 64 {
		id := NewSessionID()
		if seen[id] {
			t.Fatalf("NewSessionID() repeated %q — two topics would share one transcript", id)
		}
		seen[id] = true
	}
}
