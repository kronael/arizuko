package store

import (
	"database/sql"
	"errors"
	"time"

	"github.com/kronael/arizuko/core"
)

// RecordTurnResult inserts a row keyed on (folder, turn_id). Returns
// recorded=true on first call, false if the key already exists.
func (s *Store) RecordTurnResult(folder, turnID, sessionID, status string) (bool, error) {
	r, err := s.db.Exec(
		`INSERT OR IGNORE INTO turn_results
		 (folder, turn_id, session_id, status, recorded_at)
		 VALUES (?, ?, ?, ?, ?)`,
		folder, turnID, nilIfEmpty(sessionID), status,
		time.Now().Format(time.RFC3339Nano),
	)
	if err != nil {
		return false, err
	}
	n, _ := r.RowsAffected()
	return n == 1, nil
}

// TurnInfo is the durable summary of a round (one container spawn).
// Status is "pending" when no turn_results row exists yet.
type TurnInfo struct {
	Folder    string
	TurnID    string
	SessionID string
	Status    string
}

func (s *Store) GetTurnResult(folder, turnID string) (TurnInfo, error) {
	row := s.db.QueryRow(
		`SELECT folder, turn_id, COALESCE(session_id,''), status
		 FROM turn_results WHERE folder = ? AND turn_id = ?`,
		folder, turnID,
	)
	var ti TurnInfo
	err := row.Scan(&ti.Folder, &ti.TurnID, &ti.SessionID, &ti.Status)
	if errors.Is(err, sql.ErrNoRows) {
		return TurnInfo{Folder: folder, TurnID: turnID, Status: "pending"}, nil
	}
	if err != nil {
		return TurnInfo{}, err
	}
	return ti, nil
}

// TurnFrames returns the bot messages stamped with turnID, ordered by
// timestamp ASC. Pass afterID="" to fetch from the start; pass an
// existing message id to page forward (id > afterID by timestamp).
// Limit is clamped 1..200.
func (s *Store) TurnFrames(turnID, afterID string, limit int) ([]core.Message, error) {
	if limit <= 0 || limit > 200 {
		limit = 200
	}
	if afterID == "" {
		rows, err := s.db.Query(
			`SELECT `+msgCols+` FROM messages
			 WHERE turn_id = ? AND is_bot_message = 1
			 ORDER BY timestamp ASC, id ASC
			 LIMIT ?`,
			turnID, limit,
		)
		if err != nil {
			return nil, err
		}
		return collectMessages(rows)
	}
	var afterTs string
	if err := s.db.QueryRow(
		`SELECT timestamp FROM messages WHERE id = ?`, afterID,
	).Scan(&afterTs); err != nil {
		// Unknown afterID → return everything (caller will catch up).
		return s.TurnFrames(turnID, "", limit)
	}
	rows, err := s.db.Query(
		`SELECT `+msgCols+` FROM messages
		 WHERE turn_id = ? AND is_bot_message = 1
		   AND (timestamp > ? OR (timestamp = ? AND id > ?))
		 ORDER BY timestamp ASC, id ASC
		 LIMIT ?`,
		turnID, afterTs, afterTs, afterID, limit,
	)
	if err != nil {
		return nil, err
	}
	return collectMessages(rows)
}
