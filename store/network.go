package store

import (
	"database/sql"
	"strings"
	"time"
)

type NetworkRule struct {
	Folder    string
	Target    string
	CreatedAt time.Time
	CreatedBy string
}

func (s *Store) AddNetworkRule(folder, target, by string) error {
	_, err := s.db.Exec(
		`INSERT OR IGNORE INTO network_rules (folder, target, created_at, created_by)
		 VALUES (?, ?, ?, ?)`,
		folder, target, time.Now().UTC().Format(time.RFC3339), by,
	)
	return err
}

func (s *Store) RemoveNetworkRule(folder, target string) error {
	_, err := s.db.Exec(
		`DELETE FROM network_rules WHERE folder = ? AND target = ?`,
		folder, target,
	)
	return err
}

func (s *Store) ListNetworkRules(folder string) ([]NetworkRule, error) {
	rows, err := s.db.Query(
		`SELECT folder, target, created_at, created_by
		 FROM network_rules WHERE folder = ?
		 ORDER BY target`,
		folder,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanNetworkRules(rows)
}

func (s *Store) AllNetworkRules() ([]NetworkRule, error) {
	rows, err := s.db.Query(
		`SELECT folder, target, created_at, created_by
		 FROM network_rules ORDER BY folder, target`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanNetworkRules(rows)
}

func (s *Store) ResolveAllowlist(folder string) ([]string, error) {
	folders := folderAncestry(folder)
	placeholders := strings.Repeat("?,", len(folders))
	placeholders = placeholders[:len(placeholders)-1]
	args := make([]interface{}, len(folders))
	for i, f := range folders {
		args[i] = f
	}
	rows, err := s.db.Query(
		`SELECT DISTINCT target FROM network_rules
		 WHERE folder IN (`+placeholders+`)
		 ORDER BY target`,
		args...,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func folderAncestry(folder string) []string {
	out := []string{""}
	if folder == "" {
		return out
	}
	parts := strings.Split(folder, "/")
	cur := ""
	for _, p := range parts {
		if cur == "" {
			cur = p
		} else {
			cur = cur + "/" + p
		}
		out = append(out, cur)
	}
	return out
}

func scanNetworkRules(rows *sql.Rows) ([]NetworkRule, error) {
	var out []NetworkRule
	for rows.Next() {
		var r NetworkRule
		var ts string
		if err := rows.Scan(&r.Folder, &r.Target, &ts, &r.CreatedBy); err != nil {
			return nil, err
		}
		r.CreatedAt, _ = time.Parse(time.RFC3339, ts)
		out = append(out, r)
	}
	return out, rows.Err()
}
