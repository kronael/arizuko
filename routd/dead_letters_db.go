package routd

// dead_letters_db.go — the reads and the single mutation behind the
// dead_letters resource: one view of `messages` (status='failed' AND
// is_bot_message=1), not a new store.

import (
	"context"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/store"
)

// DeadLetters returns outbound rows routd gave up on, newest first, optionally
// narrowed to a folder subtree; 'failed' IS the dead-letter state, so no table
// is added (BUGS L23). folder=="" is the operator-wide read; the caller's gate
// decides whether it may ask for that.
func (d *DB) DeadLetters(folder string, limit int) ([]store.PendingMessage, error) {
	return d.store().DeadLetters(context.Background(), folder, limit)
}

// DeadLetterByID reads one dead-lettered row, for the containment check a replay
// needs before it touches anything.
func (d *DB) DeadLetterByID(id string) (core.Message, bool) {
	return d.store().DeadLetterByID(context.Background(), id)
}

// ReplayDeadLetter puts one row back on the delivery queue with a fresh
// attempt budget, which PendingOutbound selects, so the EXISTING sweep sends
// it. It reports whether a row moved: a row that is not dead-lettered matches
// nothing and must not read as success.
func (d *DB) ReplayDeadLetter(m core.Message) (bool, error) {
	return d.store().ReplayDeadLetter(context.Background(), m)
}
