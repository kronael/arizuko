package main

import (
	"crypto/ecdsa"
	"crypto/sha256"
	"crypto/x509"
	"database/sql"
	"embed"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"
	"time"
	"uuid"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/db_utils"
	"github.com/kronael/arizuko/obs"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

var errReuse = errors.New("refresh token reuse detected")

// keyRow is a signing key as stored, with its parsed private key.
type keyRow struct {
	key       *auth.SigningKey
	active    bool
	retiredAt *time.Time
}

func migrate(db *sql.DB) error {
	return db_utils.Migrate(db, migrationFS, "migrations", "authd")
}

// loadKeys reads every signing key and parses its private PEM.
func loadKeys(db *sql.DB) ([]keyRow, error) {
	rows, err := db.Query(`SELECT kid, priv_pem, active, retired_at FROM signing_keys`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []keyRow
	for rows.Next() {
		var kid, privPEM string
		var active int
		var retired sql.NullString
		if err := rows.Scan(&kid, &privPEM, &active, &retired); err != nil {
			return nil, err
		}
		priv, err := auth.ParseECPrivateKeyPEM(privPEM)
		if err != nil {
			return nil, fmt.Errorf("parse key %s: %w", kid, err)
		}
		kr := keyRow{key: &auth.SigningKey{Kid: kid, Priv: priv}, active: active == 1}
		if retired.Valid {
			if t, err := time.Parse(time.RFC3339, retired.String); err == nil {
				kr.retiredAt = &t
			}
		}
		out = append(out, kr)
	}
	return out, rows.Err()
}

// rotateActiveKey retires the current active key(s) as of `at` and inserts k as
// the new active key in ONE transaction. The `idx_signing_keys_one_active`
// partial-unique index means two concurrent rotations cannot both land a second
// active row: the loser's INSERT violates the constraint and its tx rolls back,
// so the trust root never splits into two signers.
func rotateActiveKey(db *sql.DB, k *auth.SigningKey, at time.Time) error {
	privPEM, pubPEM, err := encodeKey(k.Priv)
	if err != nil {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(
		`UPDATE signing_keys SET active = 0, retired_at = ? WHERE active = 1`,
		at.Format(time.RFC3339)); err != nil {
		return err
	}
	if _, err := tx.Exec(
		`INSERT INTO signing_keys (kid, priv_pem, pub_pem, active, created_at)
		 VALUES (?, ?, ?, 1, ?)`,
		k.Kid, privPEM, pubPEM, now()); err != nil {
		return err
	}
	return tx.Commit()
}

// retireActiveKeys marks all currently-active keys retired as of `at`. Used by
// emergency revoke (at backdated, window closes immediately); normal rotation
// retires in-tx via rotateActiveKey.
func retireActiveKeys(db *sql.DB, at time.Time) error {
	_, err := db.Exec(
		`UPDATE signing_keys SET active = 0, retired_at = ? WHERE active = 1`,
		at.Format(time.RFC3339))
	return err
}

// oauthIdentityForSub resolves a sub ("<provider>:<providerSub>") to the
// canonical user that has linked it, plus every sub that user has linked.
// This is the read half of upsertOAuthUser below — the model the live OAuth
// login path actually populates. It replaces identities/identity_claims, which
// this endpoint used to read and which nothing ever wrote (BUGS P2).
//
// It is also the mint-time alias resolver (oauth.go dispatch): ONE reader, so
// what /v1/identities/{sub} reports and what the token subject becomes can
// never disagree.
func oauthIdentityForSub(db *sql.DB, sub string) (userID, name, createdAt string, subs []string, ok bool) {
	provider, providerSub, found := strings.Cut(sub, ":")
	if !found || provider == "" || providerSub == "" {
		return "", "", "", nil, false
	}
	err := db.QueryRow(
		`SELECT u.user_id, u.name, u.created_at
		   FROM oauth_identities oi JOIN auth_users u ON u.user_id = oi.user_id
		  WHERE oi.provider = ? AND oi.provider_sub = ?`,
		provider, providerSub).Scan(&userID, &name, &createdAt)
	if err != nil {
		return "", "", "", nil, false
	}
	rows, err := db.Query(
		`SELECT provider, provider_sub FROM oauth_identities
		  WHERE user_id = ? ORDER BY provider`, userID)
	if err != nil {
		return "", "", "", nil, false
	}
	defer rows.Close()
	for rows.Next() {
		var p, ps string
		if err := rows.Scan(&p, &ps); err != nil {
			return "", "", "", nil, false
		}
		subs = append(subs, p+":"+ps)
	}
	if err := rows.Err(); err != nil {
		return "", "", "", nil, false
	}
	return userID, name, createdAt, subs, true
}

// upsertOAuthUser inserts a canonical user if absent and links a provider
// identity. sub is stored bare.
//
// `auth_users.user_id` IS the account's canonical sub: dispatch sets it to the
// FIRST login's "<provider>:<providerSub>" and nothing ever rewrites it (the
// ON CONFLICT below touches `name` only). That immutability is load-bearing —
// `acl.principal` rows key on this value, so a canonical that moved would
// silently strip an account of its grants. Do not add an UPDATE of user_id, and
// do not let an unlink remove the identity whose provider:provider_sub equals
// user_id: that leaves the account anchored to a login nobody holds. Refuse it
// loudly when an unlink path is built (spec 5/32 § Alias resolution).
func upsertOAuthUser(db *sql.DB, userID, name, provider, providerSub string) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(
		`INSERT INTO auth_users (user_id, name, created_at) VALUES (?, ?, ?)
		 ON CONFLICT(user_id) DO UPDATE SET name = excluded.name`,
		userID, name, now()); err != nil {
		return err
	}
	if _, err := tx.Exec(
		`INSERT INTO oauth_identities (user_id, provider, provider_sub, linked_at)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT(user_id, provider) DO UPDATE SET provider_sub = excluded.provider_sub`,
		userID, provider, providerSub, now()); err != nil {
		return err
	}
	return tx.Commit()
}

// execer is the write surface *sql.DB and *sql.Tx share, so the refresh INSERT
// has ONE owner whether it runs standalone (a new family) or inside the claim
// transaction (a rotation).
type execer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

// issueRefresh stores a new refresh token (raw returned to caller, only the
// hash persisted) starting a fresh family.
func issueRefresh(db *sql.DB, sub string, scope []string, aud string, ttl time.Duration) (string, error) {
	raw, err := insertRefresh(db, "", sub, scope, aud, ttl)
	if err != nil {
		return "", err
	}
	obs.RecordTokenMint("refresh")
	return raw, nil
}

// insertRefresh writes one refresh row into family fam (empty = new family) and
// returns the raw token. It does NOT record the mint metric: on the rotation
// path the caller must commit first, and a counter incremented by a rolled-back
// transaction is a lie.
func insertRefresh(x execer, fam, sub string, scope []string, aud string, ttl time.Duration) (string, error) {
	raw, err := genRefresh()
	if err != nil {
		return "", err
	}
	if fam == "" {
		fam = uuid.New().String()
	}
	exp := time.Now().Add(ttl)
	_, err = x.Exec(
		`INSERT INTO refresh_tokens (token_hash, family_id, sub, scope, aud, issued_at, expires_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		hashToken(raw), fam, sub, strings.Join(scope, ","), aud, now(), exp.Format(time.RFC3339))
	if err != nil {
		return "", err
	}
	return raw, nil
}

type refreshRow struct {
	family  string
	sub     string
	scope   []string
	aud     string
	expires time.Time
	used    bool
	revoked bool
}

func lookupRefresh(db *sql.DB, raw string) (refreshRow, bool) {
	var r refreshRow
	var scope, exp string
	var used, revoked sql.NullString
	err := db.QueryRow(
		`SELECT family_id, sub, scope, aud, expires_at, used_at, revoked_at
		 FROM refresh_tokens WHERE token_hash = ?`, hashToken(raw)).
		Scan(&r.family, &r.sub, &scope, &r.aud, &exp, &used, &revoked)
	if err != nil {
		return refreshRow{}, false
	}
	if scope != "" {
		r.scope = strings.Split(scope, ",")
	}
	r.expires, _ = time.Parse(time.RFC3339, exp)
	r.used = used.Valid
	r.revoked = revoked.Valid
	return r, true
}

// markRefreshUsed atomically claims a refresh token: the compare-and-set means
// exactly one of N concurrent redeems of the same token wins (rows-affected ==
// 1). Losers see won=false and must NOT rotate — a second live successor would
// fork the family and reuse-detection would never fire. The caller revokes the
// family on a lost race.
//
// The guard has TWO conjuncts and each closes a different half of BUGS F36:
//
//   - `used_at IS NULL` picks the single winner among concurrent redeems.
//   - `revoked_at IS NULL` refuses a token whose family died between the
//     caller's lookup and this statement. Without it a logout (oauth.go
//     `logout` → revokeFamily) that commits inside that gap is simply not
//     seen, the claim succeeds, and a successor is minted into a family the
//     user just killed. The transaction in claimAndRotateRefresh cannot help
//     there: a revoke that commits BEFORE the transaction opens is not an
//     interleave, so the claim itself has to look.
//
// It takes an execer so the claim can run inside that transaction.
func markRefreshUsed(x execer, raw string) (won bool, err error) {
	res, err := x.Exec(
		`UPDATE refresh_tokens SET used_at = ?
		  WHERE token_hash = ? AND used_at IS NULL AND revoked_at IS NULL`,
		now(), hashToken(raw))
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// claimAndRotateRefresh claims raw and, on a win, inserts its successor in the
// SAME transaction. This is the ordering fix for BUGS F36.
//
// The two statements used to be separate `db.Exec` calls with the grants
// re-snapshot between them, so a competing redeem's `revokeFamily` — an
// unconditional `UPDATE ... WHERE family_id = ? AND revoked_at IS NULL` —
// could commit in the gap. It revoked the rows that existed at that moment,
// the winner then inserted a successor with `revoked_at` NULL, and a family
// killed for reuse walked away with a live 30-day credential.
//
// Inside one transaction that interleave is not expressible. SQLite has a
// single writer, so a concurrent revoke either commits before the transaction
// opens — and `markRefreshUsed`'s `revoked_at IS NULL` conjunct then refuses
// the claim — or waits for the commit and revokes the successor along with the
// rest of the family. A loser cannot even reach its revoke before the winner
// commits: it learns it lost from this very UPDATE, whose write lock the
// winner holds until COMMIT.
//
// won=false means "someone else claimed it, or the family is already dead" —
// both are reuse from the caller's side, and both leave the DB untouched.
func claimAndRotateRefresh(db *sql.DB, raw, fam, sub string, scope []string, aud string, ttl time.Duration) (newRaw string, won bool, err error) {
	tx, err := db.Begin()
	if err != nil {
		return "", false, err
	}
	defer tx.Rollback()
	won, err = markRefreshUsed(tx, raw)
	if err != nil || !won {
		return "", false, err
	}
	newRaw, err = insertRefresh(tx, fam, sub, scope, aud, ttl)
	if err != nil {
		return "", false, err
	}
	if err := tx.Commit(); err != nil {
		return "", false, err
	}
	obs.RecordTokenMint("refresh")
	return newRaw, true, nil
}

func revokeFamily(db *sql.DB, fam string) error {
	_, err := db.Exec(
		`UPDATE refresh_tokens SET revoked_at = ? WHERE family_id = ? AND revoked_at IS NULL`,
		now(), fam)
	return err
}

func now() string { return time.Now().Format(time.RFC3339) }

func hashToken(t string) string {
	h := sha256.Sum256([]byte(t))
	return hex.EncodeToString(h[:])
}

func encodeKey(priv *ecdsa.PrivateKey) (privPEM, pubPEM string, err error) {
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return "", "", err
	}
	privPEM = string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	pubDER, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	if err != nil {
		return "", "", err
	}
	pubPEM = string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER}))
	return privPEM, pubPEM, nil
}
