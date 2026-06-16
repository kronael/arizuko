package store

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/kronael/arizuko/audit"
)

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
	return s.runAudited(func(tx *sql.Tx) (audit.Event, error) {
		_, err := tx.Exec(
			`INSERT INTO onboarding_gates (gate, limit_per_day, enabled)
			 VALUES (?, ?, 1)
			 ON CONFLICT(gate) DO UPDATE SET limit_per_day = excluded.limit_per_day`,
			gate, limitPerDay)
		return audit.Event{
			Category: audit.CategoryMutation,
			Action:   "gate.set",
			Actor:    "system",
			Surface:  audit.SurfaceGateway,
			Resource: "onboarding_gates/" + gate,
			Outcome:  audit.OutcomeOK,
			ParamsSummary: map[string]any{
				"gate":          gate,
				"limit_per_day": limitPerDay,
			},
		}, err
	})
}

func (s *Store) DeleteGate(gate string) error {
	return s.runAudited(func(tx *sql.Tx) (audit.Event, error) {
		_, err := tx.Exec(
			`DELETE FROM onboarding_gates WHERE gate = ?`, gate)
		return audit.Event{
			Category: audit.CategoryMutation,
			Action:   "gate.delete",
			Actor:    "system",
			Surface:  audit.SurfaceGateway,
			Resource: "onboarding_gates/" + gate,
			Outcome:  audit.OutcomeOK,
		}, err
	})
}

func (s *Store) EnableGate(gate string, enabled bool) error {
	return s.runAudited(func(tx *sql.Tx) (audit.Event, error) {
		_, err := tx.Exec(
			`UPDATE onboarding_gates SET enabled = ? WHERE gate = ?`,
			btoi(enabled), gate)
		return audit.Event{
			Category: audit.CategoryMutation,
			Action:   "gate.set",
			Actor:    "system",
			Surface:  audit.SurfaceGateway,
			Resource: "onboarding_gates/" + gate,
			Outcome:  audit.OutcomeOK,
			ParamsSummary: map[string]any{
				"enabled": enabled,
			},
		}, err
	})
}

// OnboardingRow is one row from the onboarding admission table.
type OnboardingRow struct {
	JID          string `json:"jid"`
	Status       string `json:"status"`
	UserSub      string `json:"user_sub,omitempty"`
	Gate         string `json:"gate,omitempty"`
	Created      string `json:"created"`
	PromptedAt   string `json:"prompted_at,omitempty"`
	QueuedAt     string `json:"queued_at,omitempty"`
	AdmittedAt   string `json:"admitted_at,omitempty"`
	TokenExpires string `json:"token_expires,omitempty"`
}

// ListOnboarding returns onboarding rows, optionally filtered by status. Empty
// statusFilter returns all rows. Ordered by created DESC. The token column is
// never selected (a live onboarding token is a bearer credential; spec 6/7).
func (s *Store) ListOnboarding(statusFilter string) ([]OnboardingRow, error) {
	q := `SELECT jid, status, COALESCE(user_sub,''), COALESCE(gate,''),
	             created, COALESCE(prompted_at,''), COALESCE(queued_at,''),
	             COALESCE(admitted_at,''), COALESCE(token_expires,'')
	      FROM onboarding`
	var args []any
	if statusFilter != "" {
		q += ` WHERE status = ?`
		args = append(args, statusFilter)
	}
	q += ` ORDER BY created DESC`
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []OnboardingRow
	for rows.Next() {
		var r OnboardingRow
		if err := rows.Scan(&r.JID, &r.Status, &r.UserSub, &r.Gate,
			&r.Created, &r.PromptedAt, &r.QueuedAt, &r.AdmittedAt, &r.TokenExpires); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ApproveOnboarding sets a row to approved immediately (operator fast-path,
// bypassing the gate's daily limit). Returns an error if the row does not exist.
func (s *Store) ApproveOnboarding(jid string) error {
	return s.runAudited(func(tx *sql.Tx) (audit.Event, error) {
		res, err := tx.Exec(
			`UPDATE onboarding SET status='approved', admitted_at=? WHERE jid=?`,
			time.Now().UTC().Format(time.RFC3339), jid)
		if err != nil {
			return audit.Event{}, err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return audit.Event{}, fmt.Errorf("onboarding row not found: %s", jid)
		}
		return audit.Event{
			Category: audit.CategoryMutation, Action: "onboarding.approve",
			Actor: "operator", Surface: audit.SurfaceREST,
			Resource: "onboarding/" + jid, Outcome: audit.OutcomeOK,
		}, nil
	})
}

// DenyOnboarding deletes the onboarding row for jid (operator deny).
func (s *Store) DenyOnboarding(jid string) error {
	return s.runAudited(func(tx *sql.Tx) (audit.Event, error) {
		_, err := tx.Exec(`DELETE FROM onboarding WHERE jid=?`, jid)
		return audit.Event{
			Category: audit.CategoryMutation, Action: "onboarding.deny",
			Actor: "operator", Surface: audit.SurfaceREST,
			Resource: "onboarding/" + jid, Outcome: audit.OutcomeOK,
		}, err
	})
}

// RepromptOnboarding resets a token_used row back to awaiting_message so the
// next poll tick re-sends the auth link.
func (s *Store) RepromptOnboarding(jid string) error {
	_, err := s.db.Exec(
		`UPDATE onboarding
		 SET status='awaiting_message', token=NULL, prompted_at=NULL
		 WHERE jid=?`, jid)
	return err
}
