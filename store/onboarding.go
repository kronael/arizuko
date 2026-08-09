package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/kronael/arizuko/audit"
)

// InsertOnboarding records that jid is unrouted and awaiting setup. It fires
// from routd's route-MISS branch, so it runs once per inbound message a route
// table did not claim — never for a chat that already routes.
//
// That makes it the re-greet trigger too (spec 5/31 § Deleting "greet once,
// ever"). `WHERE prompted_at IS NULL` used to be a permanent lockout: the jid
// PRIMARY KEY blocks a fresh row, so a user whose link died could never get
// another. Clearing prompted_at here arms the next poll tick, bounded two ways —
// the caller must have MISSED (a routed chat never reaches this, which is why a
// stale awaiting_message row for a since-routed chat is never greeted again),
// and the last greeting must be older than PairingTTL, so at most one live link
// exists at a time. A plain time cooldown in the poll query has neither bound
// and would re-greet every stale row every ten minutes forever.
func (s *Store) InsertOnboarding(jid string) error {
	now := time.Now().UTC()
	_, err := s.db.Exec(
		`INSERT INTO onboarding (jid, status, created)
		 VALUES (?, 'awaiting_message', ?)
		 ON CONFLICT(jid) DO UPDATE SET prompted_at = NULL
		 WHERE onboarding.status = 'awaiting_message'
		   AND onboarding.prompted_at IS NOT NULL
		   AND onboarding.prompted_at < ?`,
		jid, now.Format(time.RFC3339), now.Add(-PairingTTL).Format(time.RFC3339),
	)
	return err
}

// CarryOnboardingLegacy moves rows from the onboarding_legacy table that the
// reshaping migration leaves behind (store 0080 / onbod 0004 rename the old
// table out of the way) into the current onboarding table, then drops it.
// Idempotent: a no-op once onboarding_legacy is gone, which is the state on
// every boot after the first. Called from store.migrate (Open/OpenMem/Migrate
// all route through it) and onbod's openOwnedDB — every opener that runs the
// onboarding migrations also runs this.
//
// It carried the pre-Z3 plaintext `token` forward as hex(sha256) so already-sent
// /onboard?token=<raw> links kept resolving. That stopped buying anything at the
// 5/31 fold, which deleted every reader of the ref, and onbod 0006 drops the
// column outright — so the token columns are no longer named here at all
// (naming them would fail outright against the post-0006 schema, BUGS F40).
func CarryOnboardingLegacy(db *sql.DB) error {
	var exists int
	err := db.QueryRow(`SELECT 1 FROM sqlite_master WHERE type='table' AND name='onboarding_legacy'`).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	rows, err := tx.Query(`SELECT jid, status, prompted_at, created,
	                              user_sub, gate, queued_at, admitted_at
	                       FROM onboarding_legacy`)
	if err != nil {
		return fmt.Errorf("read onboarding_legacy: %w", err)
	}
	type legacyRow struct {
		jid, status, created string
		promptedAt, userSub,
		gate, queuedAt, admittedAt sql.NullString
	}
	var legacy []legacyRow
	for rows.Next() {
		var r legacyRow
		if err := rows.Scan(&r.jid, &r.status, &r.promptedAt, &r.created,
			&r.userSub, &r.gate, &r.queuedAt, &r.admittedAt); err != nil {
			rows.Close()
			return fmt.Errorf("scan onboarding_legacy row: %w", err)
		}
		legacy = append(legacy, r)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close() // must close before reusing tx for the inserts below

	for _, r := range legacy {
		if _, err := tx.Exec(
			`INSERT INTO onboarding (jid, status, prompted_at, created,
			                         user_sub, gate, queued_at, admitted_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			r.jid, r.status, r.promptedAt, r.created,
			r.userSub, r.gate, r.queuedAt, r.admittedAt); err != nil {
			return fmt.Errorf("carry onboarding row forward: %w", err)
		}
	}
	if _, err := tx.Exec(`DROP TABLE onboarding_legacy`); err != nil {
		return fmt.Errorf("drop onboarding_legacy: %w", err)
	}
	return tx.Commit()
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
	JID        string `json:"jid"`
	Status     string `json:"status"`
	UserSub    string `json:"user_sub,omitempty"`
	Gate       string `json:"gate,omitempty"`
	Created    string `json:"created"`
	PromptedAt string `json:"prompted_at,omitempty"`
	QueuedAt   string `json:"queued_at,omitempty"`
	AdmittedAt string `json:"admitted_at,omitempty"`
}

// ListOnboarding returns onboarding rows, optionally filtered by status. Empty
// statusFilter returns all rows. Ordered by created DESC. The setup link is not
// in this projection and has no column left to be in: the bearer lives solely
// in the link the user was sent (Z3), and token_ref/token_expires were dropped
// by onbod 0006 once the 5/31 fold left nothing writing them (BUGS F40).
func (s *Store) ListOnboarding(statusFilter string) ([]OnboardingRow, error) {
	q := `SELECT jid, status, COALESCE(user_sub,''), COALESCE(gate,''),
	             created, COALESCE(prompted_at,''), COALESCE(queued_at,''),
	             COALESCE(admitted_at,'')
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
			&r.Created, &r.PromptedAt, &r.QueuedAt, &r.AdmittedAt); err != nil {
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

// RepromptOnboarding resets an onboarding row to awaiting_message so the next
// poll tick greets it again. It is a FULL reset, admission verdict included:
// after the fold the observer only advances a row with `user_sub IS NULL`
// (spec 5/31), so leaving the old verdict's user_sub behind would re-greet the
// chat and then never admit it again — a permanent stall of exactly the kind
// BUGS O1 describes. The pairing edge is untouched; it is the user's consent,
// not admission state, and the observer re-reads it on the next tick.
//
// It survives the fold as the operator's cooldown BYPASS. The cooldown in
// InsertOnboarding is the routine reprompt; this is the button for a chat that
// is not messaging again on its own.
func (s *Store) RepromptOnboarding(jid string) error {
	_, err := s.db.Exec(
		`UPDATE onboarding
		 SET status='awaiting_message', prompted_at=NULL,
		     user_sub=NULL, gate=NULL, queued_at=NULL, admitted_at=NULL
		 WHERE jid=?`, jid)
	return err
}
