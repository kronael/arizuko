package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/groupfolder"
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
// missing, exhausted, or expired.
var ErrInviteUnavailable = errors.New("invite unavailable")

const inviteCols = `token, target_glob, issued_by_sub, issued_at, expires_at, max_uses, used_count`

func (s *Store) CreateInvite(targetGlob, issuedBySub string, maxUses int, expiresAt *time.Time) (*Invite, error) {
	if targetGlob == "" {
		return nil, errors.New("target_glob required")
	}
	// Strip trailing slash (subworld-create mode) before validating folder.
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
		`INSERT INTO invites (`+inviteCols+`)
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
	return scanInvite(s.db.QueryRow(
		`SELECT `+inviteCols+` FROM invites WHERE token = ?`, token))
}

func (s *Store) ListInvites(forIssuer string) ([]Invite, error) {
	q := `SELECT ` + inviteCols + ` FROM invites`
	var args []any
	if forIssuer != "" {
		q += ` WHERE issued_by_sub = ?`
		args = append(args, forIssuer)
	}
	q += ` ORDER BY issued_at DESC`
	rows, err := s.db.Query(q, args...)
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

// ConsumeInvite atomically increments used_count (guarding max_uses and expiry)
// and inserts an acl admin row in one transaction. Concurrent callers race on
// the UPDATE — only the first max_uses succeed.
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
		 RETURNING `+inviteCols,
		token, now)
	inv, err := scanInvite(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrInviteUnavailable
		}
		return nil, err
	}
	// Subgroup-create invites (trailing slash) do not grant folder access yet —
	// the acl row is added after username selection in handleCreateWorld.
	if !strings.HasSuffix(inv.TargetGlob, "/") {
		if _, err := tx.Exec(
			`INSERT OR IGNORE INTO acl
			 (principal, action, scope, effect, params, predicate, granted_at, granted_by)
			 VALUES (?, 'admin', ?, 'allow', '', '', datetime('now'), 'invite')`,
			userSub, inv.TargetGlob); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return inv, nil
}
