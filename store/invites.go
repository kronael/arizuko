package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/kronael/arizuko/audit"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/groupfolder"
)

// Invite is one invite row. It carries no raw-token field — the bearer is
// generated, hashed, and returned exactly once by CreateInvite (mirrors
// route_tokens' RouteToken, which likewise has no token field). Every other
// accessor works from Ref, the row's hash-at-rest primary key.
type Invite struct {
	Ref         string
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

// ErrInviteRefUnknown is returned when a ref matches no live invite.
var ErrInviteRefUnknown = errors.New("invite ref not found")

const inviteCols = `ref, target_glob, issued_by_sub, issued_at, expires_at, max_uses, used_count`

// BackfillInviteRefs carries rows from a freshly-migrated invites_legacy table
// (the pre-I1 plaintext `token TEXT PRIMARY KEY` shape) into the hash-at-rest
// invites table (`ref TEXT PRIMARY KEY`). SQLite has no sha256(), so the SQL
// migration that produces invites_legacy (store 0077 / onbod 0003) only
// reshapes the table; this Go step does the actual hashing, one time, then
// drops invites_legacy. Idempotent: a no-op once invites_legacy is gone,
// which is the state on every boot after the first. Called from store.migrate
// (Open/OpenMem/Migrate all route through it) and onbod's openOwnedDB — every
// opener that runs store's invites migrations also runs this.
func BackfillInviteRefs(db *sql.DB) error {
	var exists int
	err := db.QueryRow(`SELECT 1 FROM sqlite_master WHERE type='table' AND name='invites_legacy'`).Scan(&exists)
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

	rows, err := tx.Query(`SELECT token, target_glob, issued_by_sub, issued_at, expires_at, max_uses, used_count FROM invites_legacy`)
	if err != nil {
		return fmt.Errorf("read invites_legacy: %w", err)
	}
	type legacyInvite struct {
		token, targetGlob, issuedBySub, issuedAt string
		expiresAt                                sql.NullString
		maxUses, usedCount                       int
	}
	var legacy []legacyInvite
	for rows.Next() {
		var r legacyInvite
		if err := rows.Scan(&r.token, &r.targetGlob, &r.issuedBySub, &r.issuedAt,
			&r.expiresAt, &r.maxUses, &r.usedCount); err != nil {
			rows.Close()
			return fmt.Errorf("scan invites_legacy row: %w", err)
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
			`INSERT INTO invites (`+inviteCols+`) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			TokenRef(r.token), r.targetGlob, r.issuedBySub, r.issuedAt,
			r.expiresAt, r.maxUses, r.usedCount); err != nil {
			return fmt.Errorf("carry invite forward: %w", err)
		}
	}
	if _, err := tx.Exec(`DROP TABLE invites_legacy`); err != nil {
		return fmt.Errorf("drop invites_legacy: %w", err)
	}
	return tx.Commit()
}

// CreateInvite mints an invite and returns the row plus the raw bearer —
// shown to the caller exactly once, never stored (mirrors route_tokens'
// GenRouteToken + InsertRouteToken split).
func (s *Store) CreateInvite(targetGlob, issuedBySub string, maxUses int, expiresAt *time.Time) (*Invite, string, error) {
	if targetGlob == "" {
		return nil, "", errors.New("target_glob required")
	}
	// Strip trailing slash (subworld-create mode) before validating folder.
	check := strings.TrimSuffix(targetGlob, "/")
	if check == "" {
		check = "/"
	}
	if check != "/" && !groupfolder.IsValidFolder(check) {
		return nil, "", fmt.Errorf("invalid target_glob %q", targetGlob)
	}
	if issuedBySub == "" {
		return nil, "", errors.New("issued_by_sub required")
	}
	if maxUses < 1 {
		maxUses = 1
	}
	token := core.GenHexToken()
	ref := TokenRef(token)
	now := time.Now().UTC()
	var expStr sql.NullString
	if expiresAt != nil {
		expStr = sql.NullString{String: expiresAt.UTC().Format(time.RFC3339), Valid: true}
	}
	if err := s.runAudited(func(tx *sql.Tx) (audit.Event, error) {
		_, err := tx.Exec(
			`INSERT INTO invites (`+inviteCols+`)
			 VALUES (?, ?, ?, ?, ?, ?, 0)`,
			ref, targetGlob, issuedBySub, now.Format(time.RFC3339), expStr, maxUses)
		return audit.Event{
			Category: audit.CategoryAuthN,
			Action:   "invite.create",
			Actor:    "user:" + issuedBySub,
			ActorSub: issuedBySub,
			Surface:  audit.SurfaceGateway,
			Resource: "invites/" + ref[:8],
			Outcome:  audit.OutcomeOK,
			ParamsSummary: map[string]any{
				"target_glob": targetGlob,
				"max_uses":    maxUses,
			},
		}, err
	}); err != nil {
		return nil, "", err
	}
	return &Invite{
		Ref:         ref,
		TargetGlob:  targetGlob,
		IssuedBySub: issuedBySub,
		IssuedAt:    now,
		ExpiresAt:   expiresAt,
		MaxUses:     maxUses,
	}, token, nil
}

func scanInvite(row rowScanner) (*Invite, error) {
	var (
		inv       Invite
		issuedAt  string
		expiresAt sql.NullString
	)
	if err := row.Scan(&inv.Ref, &inv.TargetGlob, &inv.IssuedBySub, &issuedAt,
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

// GetInvite resolves the raw bearer token presented at redemption to its row.
func (s *Store) GetInvite(token string) (*Invite, error) {
	return scanInvite(s.db.QueryRow(
		`SELECT `+inviteCols+` FROM invites WHERE ref = ?`, TokenRef(token)))
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

// RevokeInviteByRef deletes the invite identified by ref (TokenRef) — ref IS
// the primary key (I1), so this is a direct delete, no resolve step. Unknown
// ref → ErrInviteRefUnknown, so a caller cannot mistake a no-op for a
// revocation.
func (s *Store) RevokeInviteByRef(ref string) error {
	return s.runAudited(func(tx *sql.Tx) (audit.Event, error) {
		res, err := tx.Exec(`DELETE FROM invites WHERE ref = ?`, ref)
		if err != nil {
			return audit.Event{}, err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return audit.Event{}, err
		}
		if n == 0 {
			return audit.Event{}, ErrInviteRefUnknown
		}
		return audit.Event{
			Category: audit.CategoryAuthN,
			Action:   "invite.revoke",
			Actor:    "system",
			Surface:  audit.SurfaceGateway,
			Resource: "invites/" + ref[:min(len(ref), 8)],
			Outcome:  audit.OutcomeOK,
		}, nil
	})
}

// ConsumeInvite atomically increments used_count (guarding max_uses and expiry)
// and inserts an acl admin row in one transaction. Concurrent callers race on
// the UPDATE — only the first max_uses succeed. Use in the monolith where
// invites + acl share one DB; the split (acl in routd.db, invites in onbod.db)
// uses ConsumeInviteNoGrant + a separate routd-side acl write.
func (s *Store) ConsumeInvite(token, userSub string) (*Invite, error) {
	return s.consumeInvite(token, userSub, true)
}

// ConsumeInviteNoGrant is the split twin: same atomic used_count increment +
// audit, but it does NOT write the acl grant — acl is routd-OWNED in the split,
// so the caller writes that row to routd.db separately (AddACLRow, which audits
// into routd.db's own audit_log).
func (s *Store) ConsumeInviteNoGrant(token, userSub string) (*Invite, error) {
	return s.consumeInvite(token, userSub, false)
}

// RestoreInvite reverses a ConsumeInviteNoGrant — decrements used_count — when
// the caller's downstream grant write (to routd.db in the split, a separate DB
// so the two can't share a tx) fails. Without this the invite is burned with no
// grant → the user is permanently locked out with no admin access.
func (s *Store) RestoreInvite(token string) error {
	_, err := s.db.Exec(
		`UPDATE invites SET used_count = used_count - 1 WHERE ref = ? AND used_count > 0`,
		TokenRef(token))
	return err
}

func (s *Store) consumeInvite(token, userSub string, grantACL bool) (*Invite, error) {
	if userSub == "" {
		return nil, errors.New("user_sub required")
	}
	ref := TokenRef(token)
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	now := time.Now().UTC().Format(time.RFC3339)
	row := tx.QueryRow(
		`UPDATE invites
		 SET used_count = used_count + 1
		 WHERE ref = ?
		   AND used_count < max_uses
		   AND (expires_at IS NULL OR expires_at > ?)
		 RETURNING `+inviteCols,
		ref, now)
	inv, err := scanInvite(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrInviteUnavailable
		}
		return nil, err
	}
	// The two halves of a redemption, split on the trailing slash.
	//
	// Subgroup-create invites (trailing slash) grant no folder access here — the
	// folder does not exist until the username picker names it, so there is no
	// scope to grant admin over. What they DO carry is the parent subtree the
	// operator invited this caller into, and that fact has to be recorded: it is
	// the authority handleCreateWorld derives the parent folder from. It used to
	// ride in the `pending_target` cookie, which the caller owns and could forge
	// to name any tenant's subtree (BUGS F50). The row goes in regardless of
	// grantACL — invite_redemptions is onbod-owned, so it lands in the same DB
	// the invites row just moved in, split or not.
	if strings.HasSuffix(inv.TargetGlob, "/") {
		if _, err := tx.Exec(
			`INSERT INTO invite_redemptions (user_sub, target_glob, redeemed_at)
			 VALUES (?, ?, ?)`,
			userSub, inv.TargetGlob, now); err != nil {
			return nil, err
		}
	} else if grantACL {
		if _, err := tx.Exec(
			`INSERT OR IGNORE INTO acl
			 (principal, action, scope, effect, params, predicate, granted_at, granted_by)
			 VALUES (?, 'admin', ?, 'allow', '', '', datetime('now'), 'invite')`,
			userSub, inv.TargetGlob); err != nil {
			return nil, err
		}
	}
	if err := audit.EmitInTx(context.Background(), tx, audit.Event{
		Category: audit.CategoryAuthN,
		Action:   "invite.consume",
		Actor:    "user:" + userSub,
		ActorSub: userSub,
		Surface:  audit.SurfaceGateway,
		Resource: "invites/" + ref[:8],
		Outcome:  audit.OutcomeOK,
		ParamsSummary: map[string]any{
			"target_glob": inv.TargetGlob,
			"used_count":  inv.UsedCount,
		},
	}); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return inv, nil
}
