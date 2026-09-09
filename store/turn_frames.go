package store

// turn_frames.go is the messages read a turn's frames come from: the bot rows
// one turn stamped, paged in time order.
//
// It is a `messages` read and nothing else. The turn's OUTCOME is an entity and
// lives in turn_results.go; these two shared a file because their names rhyme,
// and no statement or transaction ever spanned them.

import (
	"github.com/kronael/arizuko/core"
)

// TurnFrames returns the bot messages stamped with turnID, ordered by
// timestamp ASC. Pass afterID="" to fetch from the start; pass an
// existing message id to page forward (id > afterID by timestamp).
// Limit is clamped 1..200.
// chatJID binds the turn to its chat: a turn id alone is guessable and is not
// proof of ownership, so containment lives in the query rather than in each
// caller. Empty chatJID matches nothing.
func (s *Store) TurnFrames(chatJID, turnID, afterID string, limit int) ([]core.Message, error) {
	if limit <= 0 || limit > 200 {
		limit = 200
	}
	if chatJID == "" {
		return nil, nil
	}
	if afterID == "" {
		rows, err := s.db.Query(
			`SELECT `+msgCols+` FROM messages
			 WHERE turn_id = ? AND chat_jid = ? AND is_bot_message = 1
			 ORDER BY timestamp ASC, id ASC
			 LIMIT ?`,
			turnID, chatJID, limit,
		)
		if err != nil {
			return nil, err
		}
		return collectMessages(rows)
	}
	var afterTs string
	if err := s.db.QueryRow(
		`SELECT timestamp FROM messages WHERE id = ? AND chat_jid = ?`, afterID, chatJID,
	).Scan(&afterTs); err != nil {
		// Unknown afterID → return everything (caller will catch up).
		return s.TurnFrames(chatJID, turnID, "", limit)
	}
	rows, err := s.db.Query(
		`SELECT `+msgCols+` FROM messages
		 WHERE turn_id = ? AND chat_jid = ? AND is_bot_message = 1
		   AND (timestamp > ? OR (timestamp = ? AND id > ?))
		 ORDER BY timestamp ASC, id ASC
		 LIMIT ?`,
		turnID, chatJID, afterTs, afterTs, afterID, limit,
	)
	if err != nil {
		return nil, err
	}
	return collectMessages(rows)
}
