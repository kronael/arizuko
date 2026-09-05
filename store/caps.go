package store

import (
	"database/sql"
	"errors"
)

// Per-day spend caps. The cap lives beside the principal it bounds — a group's
// on `groups`, a user's on `user_profiles` — and never in the ledger, so
// reading a cap costs no aggregate. routd owns the ledger and the spend
// arithmetic (`routd/db.go` cost_log, `routd/budget.go` the gate); this file is
// the user half, which routd reaches through `routd/sibling_db.go` because
// `user_profiles` is store's table.

// UserCap returns the per-day cap for a user_sub in cents. Zero means uncapped,
// and a sub with NO user_profiles row is uncapped, not an error: platform senders
// (telegram:user/..., anon:...) and service:routd never own a profile row, and
// they are the callers cost attribution actually sees (BUGS L69).
func (s *Store) UserCap(userSub string) (int, error) {
	var cents int
	err := s.db.QueryRow(
		`SELECT COALESCE(cost_cap_cents_per_day, 0) FROM user_profiles WHERE sub = ?`,
		userSub).Scan(&cents)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return cents, nil
}

// SetUserCap writes the per-day cap for one user. 0 = uncapped.
func (s *Store) SetUserCap(userSub string, cents int) error {
	_, err := s.db.Exec(
		`UPDATE user_profiles SET cost_cap_cents_per_day = ? WHERE sub = ?`,
		cents, userSub)
	return err
}
