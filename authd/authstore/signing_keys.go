package authstore

// signing_keys.go holds every read and write of `signing_keys`, the trust root:
// one active key signs, and each retired key keeps verifying until its window
// closes.

import (
	"crypto/ecdsa"
	"crypto/x509"
	"database/sql"
	"encoding/pem"
	"fmt"
	"time"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/resreg/resources"
)

// StoredKey is a signing key as stored, with its parsed private key.
type StoredKey struct {
	Key       *auth.SigningKey
	Active    bool
	RetiredAt *time.Time
}

// SigningKeysWithMaterial reads every signing key WITH its private half and
// parses each PEM. It is the only reader in the tree that takes priv_pem, and
// it feeds the in-process signer; SigningKeyMetadata answers the operator's
// question without ever touching the column.
//
// It applies no serving window: the caller holds maxAccessTTL and decides which
// of these keys still verify.
func (s *Store) SigningKeysWithMaterial() ([]StoredKey, error) {
	rows, err := s.db.Query(`SELECT kid, priv_pem, active, retired_at FROM signing_keys`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StoredKey
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
		k := StoredKey{Key: &auth.SigningKey{Kid: kid, Priv: priv}, Active: active == 1}
		if retired.Valid {
			if t, err := time.Parse(time.RFC3339, retired.String); err == nil {
				k.RetiredAt = &t
			}
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// RotateActiveKey retires the current active key(s) as of `at` and inserts k as
// the new active key in ONE transaction. The `idx_signing_keys_one_active`
// partial-unique index means two concurrent rotations cannot both land a second
// active row: the loser's INSERT violates the constraint and its tx rolls back,
// so the trust root never splits into two signers.
func (s *Store) RotateActiveKey(k *auth.SigningKey, at time.Time) error {
	privPEM, pubPEM, err := EncodeKey(k.Priv)
	if err != nil {
		return err
	}
	tx, err := s.db.Begin()
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

// RetireActiveKeys marks all currently-active keys retired as of `at`. Used by
// emergency revoke (at backdated, window closes immediately); normal rotation
// retires in-tx via RotateActiveKey.
func (s *Store) RetireActiveKeys(at time.Time) error {
	_, err := s.db.Exec(
		`UPDATE signing_keys SET active = 0, retired_at = ? WHERE active = 1`,
		at.Format(time.RFC3339))
	return err
}

// SigningKeyMetadata reads the non-secret columns of signing_keys and derives
// each key's serving state.
//
// The SELECT list is the security boundary of this read. `priv_pem` is not in
// it, `pub_pem` is not in it, and there is no argument, filter or action that
// can add a column — the statement is a constant. SigningKeysWithMaterial is
// the only reader that takes priv_pem, and it is in-process and feeds the
// signer.
//
// Status is the time-based window spec 5/1 pins, computed once here rather than
// left for each reader to re-derive: a key serves while active, OR while
// now < retired_at + maxAccessTTL. `retiring` is a retired key whose
// already-issued tokens still verify; `retired` no longer verifies anything and
// is waiting on GC.
func (s *Store) SigningKeyMetadata(maxAccessTTL time.Duration, at time.Time) ([]resources.SigningKeysRow, error) {
	rows, err := s.db.Query(
		`SELECT kid, active, created_at, retired_at FROM signing_keys ORDER BY kid DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []resources.SigningKeysRow{}
	for rows.Next() {
		var (
			kid       string
			active    int
			createdAt string
			retiredAt sql.NullString
		)
		if err := rows.Scan(&kid, &active, &createdAt, &retiredAt); err != nil {
			return nil, err
		}
		r := resources.SigningKeysRow{
			Kid:       kid,
			Alg:       "ES256",
			Active:    active == 1,
			CreatedAt: createdAt,
			Status:    "retired",
		}
		if retiredAt.Valid {
			r.RetiredAt = retiredAt.String
			if t, err := time.Parse(time.RFC3339, retiredAt.String); err == nil {
				serves := t.Add(maxAccessTTL)
				r.ServesUntil = serves.Format(time.RFC3339)
				if at.Before(serves) {
					r.Status = "retiring"
				}
			}
		}
		if r.Active {
			r.Status = "active"
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// EncodeKey renders a private key into the two PEM columns signing_keys stores,
// which is this layer's serialization of a key rather than the signer's.
func EncodeKey(priv *ecdsa.PrivateKey) (privPEM, pubPEM string, err error) {
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
