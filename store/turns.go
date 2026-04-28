package store

import "time"

// RecordTurnResult inserts a row keyed on (folder, turn_id). Returns
// (recorded=true) on first call, (recorded=false) if the key already
// exists — callers use this to short-circuit duplicate persistence.
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
