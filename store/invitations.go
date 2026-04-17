package store

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"log/slog"
	"time"
)

type Invitation struct {
	Token     string
	Folder    string
	CreatedBy string
	CreatedAt time.Time
	Uses      int
	MaxUses   int
	Expires   *time.Time
}

func (s *Store) CreateInvitation(token, folder, createdBy string, maxUses int, expires *time.Time) error {
	var exp *string
	if expires != nil {
		s := expires.Format(time.RFC3339)
		exp = &s
	}
	_, err := s.db.Exec(
		`INSERT INTO invitations (token, folder, created_by, created_at, max_uses, expires)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		token, folder, createdBy, time.Now().Format(time.RFC3339), maxUses, exp,
	)
	return err
}

// ConsumeInvitation validates and consumes one use of the invitation.
// Returns the folder on success. Errors: expired, max uses reached, not found.
func (s *Store) ConsumeInvitation(token string) (string, error) {
	var inv Invitation
	var createdAt string
	var expires *string
	err := s.db.QueryRow(
		`SELECT token, folder, created_by, created_at, uses, max_uses, expires
		 FROM invitations WHERE token = ?`, token,
	).Scan(&inv.Token, &inv.Folder, &inv.CreatedBy, &createdAt, &inv.Uses, &inv.MaxUses, &expires)
	if err == sql.ErrNoRows {
		return "", fmt.Errorf("invite not found")
	}
	if err != nil {
		return "", err
	}
	if expires != nil {
		exp, _ := time.Parse(time.RFC3339, *expires)
		if !exp.IsZero() && time.Now().After(exp) {
			return "", fmt.Errorf("invite expired")
		}
	}
	if inv.Uses >= inv.MaxUses {
		return "", fmt.Errorf("invite max uses reached")
	}
	_, err = s.db.Exec(
		`UPDATE invitations SET uses = uses + 1 WHERE token = ?`, token)
	if err != nil {
		return "", err
	}
	return inv.Folder, nil
}

// CreateInvitationToken generates a token, inserts the invitation, and returns it.
func (s *Store) CreateInvitationToken(folder, createdBy string, maxUses int) string {
	b := make([]byte, 32)
	rand.Read(b)
	token := hex.EncodeToString(b)
	if err := s.CreateInvitation(token, folder, createdBy, maxUses, nil); err != nil {
		slog.Error("create invitation", "folder", folder, "err", err)
	}
	return token
}

func (s *Store) ListInvitations(folder string) ([]Invitation, error) {
	rows, err := s.db.Query(
		`SELECT token, folder, created_by, created_at, uses, max_uses, expires
		 FROM invitations WHERE folder = ? ORDER BY created_at DESC`, folder)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Invitation
	for rows.Next() {
		var inv Invitation
		var createdAt string
		var expires *string
		if err := rows.Scan(&inv.Token, &inv.Folder, &inv.CreatedBy,
			&createdAt, &inv.Uses, &inv.MaxUses, &expires); err != nil {
			return nil, err
		}
		inv.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
		if expires != nil {
			t, _ := time.Parse(time.RFC3339, *expires)
			inv.Expires = &t
		}
		out = append(out, inv)
	}
	return out, rows.Err()
}
