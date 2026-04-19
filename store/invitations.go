package store

import (
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"time"
)

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
