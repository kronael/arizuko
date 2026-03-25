package store

import (
	"database/sql"
	"encoding/json"
)

func (s *Store) GetGrants(folder string) []string {
	var raw string
	err := s.db.QueryRow(`SELECT rules FROM grant_rules WHERE folder = ?`, folder).Scan(&raw)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return nil
	}
	var rules []string
	if err := json.Unmarshal([]byte(raw), &rules); err != nil {
		return nil
	}
	return rules
}

func (s *Store) SetGrants(folder string, rules []string) error {
	raw, err := json.Marshal(rules)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(
		`INSERT INTO grant_rules (folder, rules) VALUES (?, ?)
		 ON CONFLICT(folder) DO UPDATE SET rules = excluded.rules`,
		folder, string(raw),
	)
	return err
}
