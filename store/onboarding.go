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

type OnboardingGate struct {
	Gate        string
	LimitPerDay int
	Enabled     bool
}

func (s *Store) ListGates() ([]OnboardingGate, error) {
	rows, err := s.db.Query(
		`SELECT gate, limit_per_day, enabled
		 FROM onboarding_gates ORDER BY gate`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []OnboardingGate
	for rows.Next() {
		var g OnboardingGate
		var en int
		if err := rows.Scan(&g.Gate, &g.LimitPerDay, &en); err != nil {
			return nil, err
		}
		g.Enabled = en != 0
		out = append(out, g)
	}
	return out, rows.Err()
}

func (s *Store) PutGate(gate string, limitPerDay int) error {
	_, err := s.db.Exec(
		`INSERT INTO onboarding_gates (gate, limit_per_day, enabled)
		 VALUES (?, ?, 1)
		 ON CONFLICT(gate) DO UPDATE SET limit_per_day = excluded.limit_per_day`,
		gate, limitPerDay)
	return err
}

func (s *Store) DeleteGate(gate string) error {
	_, err := s.db.Exec(
		`DELETE FROM onboarding_gates WHERE gate = ?`, gate)
	return err
}

func (s *Store) EnableGate(gate string, enabled bool) error {
	en := 0
	if enabled {
		en = 1
	}
	_, err := s.db.Exec(
		`UPDATE onboarding_gates SET enabled = ? WHERE gate = ?`,
		en, gate)
	return err
}
