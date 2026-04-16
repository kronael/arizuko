package store

import "time"

func (s *Store) InsertOnboarding(jid string) error {
	_, err := s.db.Exec(
		`INSERT OR IGNORE INTO onboarding (jid, status, created)
		 VALUES (?, 'awaiting_message', ?)`,
		jid, time.Now().Format(time.RFC3339),
	)
	return err
}
