package store

import "time"

// InsertOnboarding adds a new onboarding entry for jid with status=awaiting_world.
// Uses INSERT OR IGNORE so duplicate calls are silently skipped.
func (s *Store) InsertOnboarding(jid, sender, channel string) error {
	_, err := s.db.Exec(
		`INSERT OR IGNORE INTO onboarding (jid, status, sender, channel, created)
		 VALUES (?, 'awaiting_world', ?, ?, ?)`,
		jid, nilIfEmpty(sender), nilIfEmpty(channel), time.Now().Format(time.RFC3339),
	)
	return err
}
