package store

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"log/slog"
	"time"
)

// CreateInvitationToken generates a token, inserts the invitation, and returns it.
func (s *Store) CreateInvitationToken(folder, createdBy string, maxUses int) string {
	b := make([]byte, 32)
	rand.Read(b)
	token := hex.EncodeToString(b)
	_, err := s.db.Exec(
		`INSERT INTO invitations (token, folder, created_by, created_at, max_uses, expires)
		 VALUES (?, ?, ?, ?, ?, NULL)`,
		token, folder, createdBy, time.Now().Format(time.RFC3339), maxUses,
	)
	if err != nil {
		slog.Error("create invitation", "folder", folder, "err", err)
	}
	return token
}

// ConsumeInvitation validates and consumes one use of the invitation.
// Returns the folder on success. Errors: expired, max uses reached, not found.
func (s *Store) ConsumeInvitation(token string) (string, error) {
	var folder string
	var uses, maxUses int
	var expires *string
	err := s.db.QueryRow(
		`SELECT folder, uses, max_uses, expires FROM invitations WHERE token = ?`, token,
	).Scan(&folder, &uses, &maxUses, &expires)
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
	if uses >= maxUses {
		return "", fmt.Errorf("invite max uses reached")
	}
	if _, err := s.db.Exec(
		`UPDATE invitations SET uses = uses + 1 WHERE token = ?`, token); err != nil {
		return "", err
	}
	return folder, nil
}
