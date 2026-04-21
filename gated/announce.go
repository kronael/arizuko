package main

import (
	"database/sql"
	"fmt"
	"strings"

	"github.com/onvos/arizuko/store"
)

// announceStartup writes one system message into the root group listing
// pending migration announcements. Deduplicates by checking for an
// existing unread (unflushed) migration system message first.
func announceStartup(s *store.Store) error {
	db := s.DB()
	root, err := rootFolder(db)
	if err != nil || root == "" {
		return err
	}
	jids, err := knownJIDs(db)
	if err != nil {
		return err
	}
	pend, err := s.PendingAnnouncements(jids)
	if err != nil || len(pend) == 0 {
		return err
	}
	if hasPendingMigrationSysMsg(db, root) {
		return nil
	}
	var b strings.Builder
	b.WriteString("Pending announcements:\n")
	for _, a := range pend {
		fmt.Fprintf(&b, "- %s v%d\n", a.Service, a.Version)
	}
	return s.EnqueueSysMsg(root, "migration", "pending", b.String())
}

// rootFolder returns the folder of the single root group (no parent).
// Simplest correct shape: first active group with empty parent.
func rootFolder(db *sql.DB) (string, error) {
	var folder string
	err := db.QueryRow(
		`SELECT folder FROM groups
		 WHERE (parent IS NULL OR parent = '')
		 ORDER BY added_at ASC LIMIT 1`,
	).Scan(&folder)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return folder, err
}

// knownJIDs returns every jid arizuko has ever seen in chats.
func knownJIDs(db *sql.DB) ([]string, error) {
	rows, err := db.Query(`SELECT jid FROM chats`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var j string
		if err := rows.Scan(&j); err == nil {
			out = append(out, j)
		}
	}
	return out, rows.Err()
}

func hasPendingMigrationSysMsg(db *sql.DB, folder string) bool {
	var n int
	db.QueryRow(
		`SELECT COUNT(*) FROM system_messages
		 WHERE group_id = ? AND origin = 'migration'`,
		folder,
	).Scan(&n)
	return n > 0
}
