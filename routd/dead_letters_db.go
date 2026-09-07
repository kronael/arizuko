package routd

// dead_letters_db.go — the reads and the single mutation behind routd's
// dead_letters resource. They sit next to that resource rather than in db.go
// because they are one view of `messages` (status='failed' AND
// is_bot_message=1), not a new store.

import (
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/groupfolder"
)

// DeadLetters returns outbound rows routd gave up on, newest first, optionally
// narrowed to a folder subtree. 'failed' IS the dead-letter state and the row
// keeps its content, so this is a read over existing state — no dead-letter
// table exists and none is added (BUGS L23).
//
// folder=="" means every folder, which is the operator-wide read; the caller's
// gate decides whether it may ask for that.
func (d *DB) DeadLetters(folder string, limit int) ([]core.Message, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	q := `SELECT ` + msgReadCols + `
		FROM messages WHERE status=? AND is_bot_message=1`
	args := []any{core.MessageStatusFailed}
	if folder != "" {
		// Subtree, not exact: a grant on `acme` covers `acme/support`. The
		// predicate is substr, never LIKE — `_` and `%` are legal in a folder
		// name and LIKE folds ASCII case, so LIKE hands one folder's failed
		// messages to a neighbour's grant (BUGS L252).
		q += ` AND ` + groupfolder.SQLSubtree("routed_to", "/")
		args = append(args, groupfolder.SubtreeArgs(folder, "/")...)
	}
	q += ` ORDER BY timestamp DESC LIMIT ?`
	args = append(args, limit)
	rows, err := d.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	msgs, _, err := scanMessages(rows, "")
	return msgs, err
}

// DeadLetterByID reads one dead-lettered row, for the containment check a
// replay needs before it touches anything.
func (d *DB) DeadLetterByID(id string) (core.Message, bool) {
	rows, err := d.db.Query(`SELECT `+msgReadCols+`
		FROM messages WHERE id=? AND status=? AND is_bot_message=1`,
		id, core.MessageStatusFailed)
	if err != nil {
		return core.Message{}, false
	}
	defer rows.Close()
	msgs, _, err := scanMessages(rows, "")
	if err != nil || len(msgs) == 0 {
		return core.Message{}, false
	}
	return msgs[0], true
}

// ReplayDeadLetter puts one dead-lettered row back on the delivery queue:
// status→'pending' with a fresh attempt budget, which is exactly what
// PendingOutbound selects, so routd's EXISTING outbound sweep sends it. No
// second delivery path is introduced. Reports whether a row moved — a row that
// is not dead-lettered (already sent, or replayed a moment ago by someone else)
// matches nothing and must not read as success.
func (d *DB) ReplayDeadLetter(id string) (bool, error) {
	res, err := d.db.Exec(
		`UPDATE messages SET status=?, delivery_attempts=0
		 WHERE id=? AND status=? AND is_bot_message=1`,
		core.MessageStatusPending, id, core.MessageStatusFailed)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}
