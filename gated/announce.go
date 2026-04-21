package main

import (
	"database/sql"
	"fmt"
	"strings"

	"github.com/onvos/arizuko/store"
)

// announceStartup writes one system message into the root group listing
// pending migration announcements. Deduplicates by skipping if an
// unflushed migration sysmsg already exists.
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
	if !s.AnyPending(jids) {
		return nil
	}
	if hasPendingMigrationSysMsg(db, root) {
		return nil
	}
	var b strings.Builder
	b.WriteString("Pending announcements. Fan each out to every jid in `chats` " +
		"not yet recorded in `announcement_sent`, then record delivery.\n\n")
	for _, a := range s.Announcements() {
		fmt.Fprintf(&b, "<announcement service=%q version=%d>\n%s\n</announcement>\n",
			a.Service, a.Version, strings.TrimSpace(a.Body))
	}
	return s.EnqueueSysMsg(root, "migration", "pending", b.String())
}

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
