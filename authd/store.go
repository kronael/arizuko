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

	"github.com/google/uuid"
	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/db_utils"
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
		priv, err := parseECPriv(privPEM)
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

// createUser inserts a canonical user if absent and links a provider identity.
// sub is stored bare. Returns the canonical user_id.
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

func userExists(db *sql.DB, userID string) bool {
	var n int
	db.QueryRow(`SELECT 1 FROM auth_users WHERE user_id = ?`, userID).Scan(&n)
	return n == 1
}

// issueRefresh stores a new refresh token (raw returned to caller, only the
// hash persisted) starting a fresh family.
func issueRefresh(db *sql.DB, sub string, scope []string, aud string, ttl time.Duration) (string, error) {
	return rotateRefresh(db, "", sub, scope, aud, ttl)
}

// rotateRefresh stores a successor refresh token in family fam (empty = new
// family). Returns the raw token.
func rotateRefresh(db *sql.DB, fam, sub string, scope []string, aud string, ttl time.Duration) (string, error) {
	raw, err := genRefresh()
	if err != nil {
		return "", err
	}
	if fam == "" {
		fam = uuid.NewString()
	}
	exp := time.Now().Add(ttl)
	_, err = db.Exec(
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

// markRefreshUsed atomically claims a refresh token: the compare-and-set
// `used_at IS NULL` guard means exactly one of N concurrent redeems of the same
// token wins (rows-affected == 1). Losers see won=false and must NOT rotate —
// a second live successor would fork the family and reuse-detection would never
// fire. The caller revokes the family on a lost race.
func markRefreshUsed(db *sql.DB, raw string) (won bool, err error) {
	res, err := db.Exec(
		`UPDATE refresh_tokens SET used_at = ? WHERE token_hash = ? AND used_at IS NULL`,
		now(), hashToken(raw))
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
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

func parseECPriv(privPEM string) (*ecdsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(privPEM))
	if block == nil {
		return nil, errors.New("no PEM block")
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	ec, ok := key.(*ecdsa.PrivateKey)
	if !ok {
		return nil, errors.New("not an ECDSA key")
	}
	return ec, nil
}
