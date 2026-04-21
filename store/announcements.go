package store

import (
	"database/sql"
	"time"
)

// Announcement is a pending upgrade note paired to a migration.
type Announcement struct {
	Service string
	Version int
	Body    string
}

// PendingAnnouncements returns announcements with at least one known jid
// that hasn't yet received them. Caller supplies the jid universe.
func (s *Store) PendingAnnouncements(jids []string) ([]Announcement, error) {
	return pendingAnnouncements(s.db, jids)
}

func pendingAnnouncements(db *sql.DB, jids []string) ([]Announcement, error) {
	rows, err := db.Query(
		`SELECT service, version, body FROM announcements
		 ORDER BY service, version`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var all []Announcement
	for rows.Next() {
		var a Announcement
		if err := rows.Scan(&a.Service, &a.Version, &a.Body); err != nil {
			return nil, err
		}
		all = append(all, a)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var out []Announcement
	for _, a := range all {
		if hasPendingJID(db, a, jids) {
			out = append(out, a)
		}
	}
	return out, nil
}

func hasPendingJID(db *sql.DB, a Announcement, jids []string) bool {
	for _, j := range jids {
		var n int
		db.QueryRow(
			`SELECT COUNT(*) FROM announcement_sent
			 WHERE service=? AND version=? AND user_jid=?`,
			a.Service, a.Version, j,
		).Scan(&n)
		if n == 0 {
			return true
		}
	}
	return false
}

// RecordAnnouncementSent writes a ledger row for (service, version, jid).
func (s *Store) RecordAnnouncementSent(service string, version int, jid string) error {
	_, err := s.db.Exec(
		`INSERT OR IGNORE INTO announcement_sent
		 (service, version, user_jid, sent_at) VALUES (?,?,?,?)`,
		service, version, jid, time.Now().Format(time.RFC3339),
	)
	return err
}

// InsertAnnouncement is used by tests that bypass the migration path.
func (s *Store) InsertAnnouncement(a Announcement) error {
	_, err := s.db.Exec(
		`INSERT INTO announcements (service, version, body, created_at)
		 VALUES (?,?,?,?)`,
		a.Service, a.Version, a.Body, time.Now().Format(time.RFC3339),
	)
	return err
}
