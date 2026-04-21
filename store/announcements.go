package store

import (
	"io/fs"
	"strconv"
	"strings"
	"time"
)

// Announcement is a migration-paired upgrade note sourced from
// store/migrations/NNNN-*.md. Files are the source of truth; the DB
// only tracks per-jid delivery in announcement_sent.
type Announcement struct {
	Service string
	Version int
	Body    string
}

// Announcements reads all paired `.md` files from the embedded
// migrations FS. Version = 4-digit filename prefix.
func (s *Store) Announcements() []Announcement {
	return scanAnnouncements(migrationFS, "migrations", serviceName)
}

func scanAnnouncements(fsys fs.FS, dir, service string) []Announcement {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil
	}
	var out []Announcement
	for _, e := range entries {
		n := e.Name()
		if !strings.HasSuffix(n, ".md") || len(n) < 5 {
			continue
		}
		ver, err := strconv.Atoi(n[:4])
		if err != nil {
			continue
		}
		body, err := fs.ReadFile(fsys, dir+"/"+n)
		if err != nil {
			continue
		}
		out = append(out, Announcement{Service: service, Version: ver, Body: string(body)})
	}
	return out
}

// UnsentTo returns announcements not yet recorded as delivered to jid.
func (s *Store) UnsentTo(jid string) []Announcement {
	var out []Announcement
	for _, a := range s.Announcements() {
		var n int
		s.db.QueryRow(
			`SELECT COUNT(*) FROM announcement_sent
			 WHERE service=? AND version=? AND user_jid=?`,
			a.Service, a.Version, jid,
		).Scan(&n)
		if n == 0 {
			out = append(out, a)
		}
	}
	return out
}

// AnyPending reports whether at least one jid is still owed at least
// one announcement.
func (s *Store) AnyPending(jids []string) bool {
	for _, j := range jids {
		if len(s.UnsentTo(j)) > 0 {
			return true
		}
	}
	return false
}

// RecordSent writes one ledger row for (service, version, jid).
func (s *Store) RecordSent(service string, version int, jid string) error {
	_, err := s.db.Exec(
		`INSERT OR IGNORE INTO announcement_sent
		 (service, version, user_jid, sent_at) VALUES (?,?,?,?)`,
		service, version, jid, time.Now().Format(time.RFC3339),
	)
	return err
}
