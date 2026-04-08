package store

import "time"

// InsertOnboarding adds a new onboarding entry for jid with status=awaiting_message.
// Uses INSERT OR IGNORE so duplicate calls are silently skipped.
func (s *Store) InsertOnboarding(jid string) error {
	_, err := s.db.Exec(
		`INSERT OR IGNORE INTO onboarding (jid, status, created)
		 VALUES (?, 'awaiting_message', ?)`,
		jid, time.Now().Format(time.RFC3339),
	)
	return err
}
