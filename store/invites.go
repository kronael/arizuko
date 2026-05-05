package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/onvos/arizuko/core"
	"github.com/onvos/arizuko/groupfolder"
)

type Invite struct {
	Token       string
	TargetGlob  string
	IssuedBySub string
	IssuedAt    time.Time
	ExpiresAt   *time.Time
	MaxUses     int
	UsedCount   int
}

// ErrInviteUnavailable is returned by ConsumeInvite when the token is
// missing, exhausted, or expired. Callers render a single user-facing
// "invalid or used" page; the cause is logged at the call site.
var ErrInviteUnavailable = errors.New("invite unavailable")

func (s *Store) CreateInvite(targetGlob, issuedBySub string, maxUses int, expiresAt *time.Time) (*Invite, error) {
	if targetGlob == "" {
		return nil, errors.New("target_glob required")
	}
	// Validate: strip trailing slash (subworld-create mode uses "parent/")
	// before checking folder validity.
	check := strings.TrimSuffix(targetGlob, "/")
	if check == "" {
		check = "/"
	}
	if check != "/" && !groupfolder.IsValidFolder(check) {
		return nil, fmt.Errorf("invalid target_glob %q", targetGlob)
	}
	if issuedBySub == "" {
		return nil, errors.New("issued_by_sub required")
	}
	if maxUses < 1 {
		maxUses = 1
	}
	token := core.GenHexToken()
	now := time.Now().UTC()
	var expStr sql.NullString
	if expiresAt != nil {
		expStr = sql.NullString{String: expiresAt.UTC().Format(time.RFC3339), Valid: true}
	}
	_, err := s.db.Exec(
		`INSERT INTO invites (token, target_glob, issued_by_sub, issued_at, expires_at, max_uses, used_count)
		 VALUES (?, ?, ?, ?, ?, ?, 0)`,
		token, targetGlob, issuedBySub, now.Format(time.RFC3339), expStr, maxUses)
	if err != nil {
		return nil, err
	}
	return &Invite{
		Token:       token,
		TargetGlob:  targetGlob,
		IssuedBySub: issuedBySub,
		IssuedAt:    now,
		ExpiresAt:   expiresAt,
		MaxUses:     maxUses,
		UsedCount:   0,
	}, nil
}

func scanInvite(row rowScanner) (*Invite, error) {
	var (
		inv       Invite
		issuedAt  string
		expiresAt sql.NullString
	)
	if err := row.Scan(&inv.Token, &inv.TargetGlob, &inv.IssuedBySub, &issuedAt,
		&expiresAt, &inv.MaxUses, &inv.UsedCount); err != nil {
		return nil, err
	}
	inv.IssuedAt, _ = time.Parse(time.RFC3339, issuedAt)
	if expiresAt.Valid && expiresAt.String != "" {
		t, _ := time.Parse(time.RFC3339, expiresAt.String)
		inv.ExpiresAt = &t
	}
	return &inv, nil
}

func (s *Store) GetInvite(token string) (*Invite, error) {
	row := s.db.QueryRow(
		`SELECT token, target_glob, issued_by_sub, issued_at, expires_at, max_uses, used_count
		 FROM invites WHERE token = ?`, token)
	return scanInvite(row)
}

// ListInvites returns all invites; if forIssuer != "" only those rows.
func (s *Store) ListInvites(forIssuer string) ([]Invite, error) {
	var (
		rows *sql.Rows
		err  error
	)
	if forIssuer != "" {
		rows, err = s.db.Query(
			`SELECT token, target_glob, issued_by_sub, issued_at, expires_at, max_uses, used_count
			 FROM invites WHERE issued_by_sub = ? ORDER BY issued_at DESC`, forIssuer)
	} else {
		rows, err = s.db.Query(
			`SELECT token, target_glob, issued_by_sub, issued_at, expires_at, max_uses, used_count
			 FROM invites ORDER BY issued_at DESC`)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Invite
	for rows.Next() {
		inv, err := scanInvite(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *inv)
	}
	return out, rows.Err()
}

func (s *Store) RevokeInvite(token string) error {
	_, err := s.db.Exec(`DELETE FROM invites WHERE token = ?`, token)
	return err
}

// ConsumeInvite atomically increments used_count (guarding both max_uses
// and expiry) and inserts a user_groups row in one transaction. Concurrent
// callers race on the UPDATE — only the first max_uses succeed.
// Returns ErrInviteUnavailable when the token is missing, exhausted, or
// expired.
func (s *Store) ConsumeInvite(token, userSub string) (*Invite, error) {
	if userSub == "" {
		return nil, errors.New("user_sub required")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	now := time.Now().UTC().Format(time.RFC3339)
	row := tx.QueryRow(
		`UPDATE invites
		 SET used_count = used_count + 1
		 WHERE token = ?
		   AND used_count < max_uses
		   AND (expires_at IS NULL OR expires_at > ?)
		 RETURNING token, target_glob, issued_by_sub, issued_at, expires_at, max_uses, used_count`,
		token, now)
	inv, err := scanInvite(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrInviteUnavailable
		}
		return nil, err
	}
	if _, err := tx.Exec(
		`INSERT OR IGNORE INTO user_groups (user_sub, folder, granted_at)
		 VALUES (?, ?, datetime('now'))`,
		userSub, inv.TargetGlob); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return inv, nil
}
