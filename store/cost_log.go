package store

import "time"

// CostRow is one LLM call's accounting row written into cost_log.
// Spec 5/34. Folder and UserSub are how the budget gate aggregates;
// either may be empty (channel-scoped vs identified-caller turns).
type CostRow struct {
	TS         time.Time
	Folder     string
	UserSub    string
	Model      string
	InputTok   int
	CacheRead  int
	CacheWrite int
	OutputTok  int
	Cents      int
}

func (s *Store) LogCost(r CostRow) error {
	ts := r.TS
	if ts.IsZero() {
		ts = time.Now().UTC()
	}
	_, err := s.db.Exec(
		`INSERT INTO cost_log
		 (ts, folder, user_sub, model, input_tok, cache_read, cache_write, output_tok, cents)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		ts.Format(time.RFC3339Nano), r.Folder, r.UserSub, r.Model,
		r.InputTok, r.CacheRead, r.CacheWrite, r.OutputTok, r.Cents)
	return err
}

// SpendTodayFolder returns the sum of cents logged for `folder` since the
// UTC start of today. Empty folder is treated literally — caller decides
// whether to query the empty-folder bucket.
func (s *Store) SpendTodayFolder(folder string) (int, error) {
	return s.spendSince("folder", folder, startOfTodayUTC())
}

// SpendTodayUser returns the sum of cents logged for `userSub` since the
// UTC start of today. Per-user view composes with per-channel caps.
func (s *Store) SpendTodayUser(userSub string) (int, error) {
	return s.spendSince("user_sub", userSub, startOfTodayUTC())
}

func (s *Store) spendSince(col, val string, since time.Time) (int, error) {
	var cents int
	err := s.db.QueryRow(
		`SELECT COALESCE(SUM(cents), 0) FROM cost_log
		 WHERE `+col+` = ? AND ts >= ?`,
		val, since.Format(time.RFC3339Nano)).Scan(&cents)
	if err != nil {
		return 0, err
	}
	return cents, nil
}

// FolderCap returns the per-day cap for a group folder in cents.
// Zero means uncapped (default). Spec 5/34.
func (s *Store) FolderCap(folder string) (int, error) {
	var cents int
	err := s.db.QueryRow(
		`SELECT COALESCE(cost_cap_cents_per_day, 0) FROM groups WHERE folder = ?`,
		folder).Scan(&cents)
	if err != nil {
		return 0, err
	}
	return cents, nil
}

// SetFolderCap writes the per-day cap on the groups row for folder.
// 0 = uncapped. Operator surface; never called from agent path.
func (s *Store) SetFolderCap(folder string, cents int) error {
	_, err := s.db.Exec(
		`UPDATE groups SET cost_cap_cents_per_day = ? WHERE folder = ?`,
		cents, folder)
	return err
}

// UserCap returns the per-day cap for a user_sub in cents.
func (s *Store) UserCap(userSub string) (int, error) {
	var cents int
	err := s.db.QueryRow(
		`SELECT COALESCE(cost_cap_cents_per_day, 0) FROM auth_users WHERE sub = ?`,
		userSub).Scan(&cents)
	if err != nil {
		return 0, err
	}
	return cents, nil
}

// SetUserCap writes the per-day cap for one user. 0 = uncapped.
func (s *Store) SetUserCap(userSub string, cents int) error {
	_, err := s.db.Exec(
		`UPDATE auth_users SET cost_cap_cents_per_day = ? WHERE sub = ?`,
		cents, userSub)
	return err
}

func startOfTodayUTC() time.Time {
	now := time.Now().UTC()
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
}
