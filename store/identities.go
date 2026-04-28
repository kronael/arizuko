package store

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

type Identity struct {
	ID        string
	Name      string
	CreatedAt time.Time
}

type IdentityClaim struct {
	Sub        string
	IdentityID string
	ClaimedAt  time.Time
}

var ErrLinkCodeInvalid = errors.New("link code invalid or expired")

// CreateIdentity inserts a fresh identity row. ID is a 16-byte hex token.
func (s *Store) CreateIdentity(name string) (Identity, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return Identity{}, fmt.Errorf("rand: %w", err)
	}
	id := hex.EncodeToString(b)
	now := time.Now()
	if _, err := s.db.Exec(
		`INSERT INTO identities (id, name, created_at) VALUES (?, ?, ?)`,
		id, name, now.Format(time.RFC3339)); err != nil {
		return Identity{}, err
	}
	return Identity{ID: id, Name: name, CreatedAt: now}, nil
}

func (s *Store) GetIdentity(id string) (Identity, bool) {
	var idn Identity
	var created string
	err := s.db.QueryRow(
		`SELECT id, name, created_at FROM identities WHERE id = ?`, id,
	).Scan(&idn.ID, &idn.Name, &created)
	if err != nil {
		return Identity{}, false
	}
	idn.CreatedAt, _ = time.Parse(time.RFC3339, created)
	return idn, true
}

// GetIdentityForSub returns the identity claimed by sub plus all claimed subs
// for that identity. ok=false when sub has no claim.
func (s *Store) GetIdentityForSub(sub string) (Identity, []string, bool) {
	var idID string
	if err := s.db.QueryRow(
		`SELECT identity_id FROM identity_claims WHERE sub = ?`, sub,
	).Scan(&idID); err != nil {
		return Identity{}, nil, false
	}
	idn, ok := s.GetIdentity(idID)
	if !ok {
		return Identity{}, nil, false
	}
	subs, _ := s.SubsForIdentity(idID)
	return idn, subs, true
}

func (s *Store) SubsForIdentity(identityID string) ([]string, error) {
	rows, err := s.db.Query(
		`SELECT sub FROM identity_claims WHERE identity_id = ? ORDER BY claimed_at`,
		identityID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var sub string
		if err := rows.Scan(&sub); err != nil {
			return nil, err
		}
		out = append(out, sub)
	}
	return out, rows.Err()
}

func (s *Store) ListIdentities() ([]Identity, error) {
	rows, err := s.db.Query(
		`SELECT id, name, created_at FROM identities ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Identity
	for rows.Next() {
		var idn Identity
		var created string
		if err := rows.Scan(&idn.ID, &idn.Name, &created); err != nil {
			return nil, err
		}
		idn.CreatedAt, _ = time.Parse(time.RFC3339, created)
		out = append(out, idn)
	}
	return out, rows.Err()
}

// LinkSub binds sub to identityID. If sub is already claimed by another
// identity, the claim is rebound (REPLACE). Caller is responsible for
// authorizing the rebind — store layer is mechanical.
func (s *Store) LinkSub(identityID, sub string) error {
	_, err := s.db.Exec(
		`INSERT OR REPLACE INTO identity_claims (sub, identity_id, claimed_at)
		 VALUES (?, ?, ?)`,
		sub, identityID, time.Now().Format(time.RFC3339))
	return err
}

func (s *Store) UnlinkSub(sub string) (bool, error) {
	res, err := s.db.Exec(
		`DELETE FROM identity_claims WHERE sub = ?`, sub)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// MintLinkCode creates a one-shot code for identityID. ttl is the validity
// window from now. Returns the code string.
func (s *Store) MintLinkCode(identityID string, ttl time.Duration) (string, error) {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("rand: %w", err)
	}
	code := "link-" + hex.EncodeToString(b)
	if _, err := s.db.Exec(
		`INSERT INTO identity_codes (code, identity_id, expires_at)
		 VALUES (?, ?, ?)`,
		code, identityID, time.Now().Add(ttl).Format(time.RFC3339)); err != nil {
		return "", err
	}
	return code, nil
}

// ConsumeLinkCode atomically claims sub for the identity associated with
// code, if the code exists and hasn't expired. The code is single-shot:
// successful consume deletes the row. Expired codes are also pruned.
// Returns the identity_id on success, ErrLinkCodeInvalid otherwise.
func (s *Store) ConsumeLinkCode(code, sub string) (string, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()

	var idID, expires string
	err = tx.QueryRow(
		`SELECT identity_id, expires_at FROM identity_codes WHERE code = ?`,
		code).Scan(&idID, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrLinkCodeInvalid
	}
	if err != nil {
		return "", err
	}

	exp, _ := time.Parse(time.RFC3339, expires)
	if !exp.IsZero() && time.Now().After(exp) {
		_, _ = tx.Exec(`DELETE FROM identity_codes WHERE code = ?`, code)
		_ = tx.Commit()
		return "", ErrLinkCodeInvalid
	}

	if _, err := tx.Exec(
		`INSERT OR REPLACE INTO identity_claims (sub, identity_id, claimed_at)
		 VALUES (?, ?, ?)`,
		sub, idID, time.Now().Format(time.RFC3339)); err != nil {
		return "", err
	}
	if _, err := tx.Exec(
		`DELETE FROM identity_codes WHERE code = ?`, code); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return idID, nil
}

// PruneExpiredLinkCodes deletes identity_codes whose expires_at is in
// the past. Cheap; called from background sweepers if any.
func (s *Store) PruneExpiredLinkCodes() (int64, error) {
	res, err := s.db.Exec(
		`DELETE FROM identity_codes WHERE expires_at < ?`,
		time.Now().Format(time.RFC3339))
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}
