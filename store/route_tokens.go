package store

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"strings"
	"time"
)

// RouteToken maps a bearer token to a single inbound JID + admin folder.
// Spec 5/W. JID kind is encoded in the prefix:
//
//   - web:<folder>[/<suffix>]      — anonymous web chat (chat link)
//   - hook:<folder>/<source>[...]  — labeled webhook ingest
//
// owner_folder bounds revocation (admin authority); it may diverge from
// the folder embedded in the JID when a higher-tier agent mints on
// behalf of a descendant.
type RouteToken struct {
	JID         string
	OwnerFolder string
	CreatedAt   time.Time
}

const (
	RouteTokenKindChat = "chat" // jid prefix "web:"
	RouteTokenKindHook = "hook" // jid prefix "hook:"
)

// GenRouteToken returns a 256-bit base64url-encoded random token.
// The raw token is returned to the caller exactly once; only the
// sha256 hash is persisted.
func GenRouteToken() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(fmt.Sprintf("crypto/rand failed: %v", err))
	}
	return base64.RawURLEncoding.EncodeToString(b[:])
}

func hashRouteToken(raw string) []byte {
	sum := sha256.Sum256([]byte(raw))
	return sum[:]
}

// RouteTokenKind returns "chat" for web: jids, "hook" for hook: jids, "" otherwise.
func RouteTokenKind(jid string) string {
	switch {
	case strings.HasPrefix(jid, "web:"):
		return RouteTokenKindChat
	case strings.HasPrefix(jid, "hook:"):
		return RouteTokenKindHook
	default:
		return ""
	}
}

// InsertRouteToken persists a new token. Caller chooses the raw token
// (via GenRouteToken) and is responsible for returning it to the user;
// the store only retains sha256(raw).
func (s *Store) InsertRouteToken(rawToken string, t RouteToken) error {
	if rawToken == "" {
		return fmt.Errorf("empty token")
	}
	if t.JID == "" || t.OwnerFolder == "" {
		return fmt.Errorf("jid and owner_folder required")
	}
	if RouteTokenKind(t.JID) == "" {
		return fmt.Errorf("jid must start with web: or hook:")
	}
	ts := t.CreatedAt
	if ts.IsZero() {
		ts = time.Now()
	}
	_, err := s.db.Exec(
		`INSERT INTO route_tokens (token_hash, jid, owner_folder, created_at) VALUES (?, ?, ?, ?)`,
		hashRouteToken(rawToken), t.JID, t.OwnerFolder, ts.Format(time.RFC3339Nano),
	)
	return err
}

// LookupRouteToken resolves a raw bearer token to its row. Returns
// (zero, false) when the token is unknown.
func (s *Store) LookupRouteToken(rawToken string) (RouteToken, bool) {
	if rawToken == "" {
		return RouteToken{}, false
	}
	var t RouteToken
	var createdAt string
	err := s.db.QueryRow(
		`SELECT jid, owner_folder, created_at FROM route_tokens WHERE token_hash = ?`,
		hashRouteToken(rawToken),
	).Scan(&t.JID, &t.OwnerFolder, &createdAt)
	if err != nil {
		return RouteToken{}, false
	}
	t.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	return t, true
}

// ListRouteTokens returns all tokens owned by ownerFolder.
func (s *Store) ListRouteTokens(ownerFolder string) []RouteToken {
	rows, err := s.db.Query(
		`SELECT jid, owner_folder, created_at FROM route_tokens WHERE owner_folder = ? ORDER BY created_at DESC`,
		ownerFolder,
	)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []RouteToken
	for rows.Next() {
		var t RouteToken
		var createdAt string
		if rows.Scan(&t.JID, &t.OwnerFolder, &createdAt) != nil {
			continue
		}
		t.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
		out = append(out, t)
	}
	return out
}

// RevokeRouteToken deletes tokens with the given jid owned by ownerFolder.
// Returns (true, nil) if at least one row was deleted, (false, nil) when
// no matching row existed. ACL scope: the caller's `owner_folder` MUST
// match — agents in folder A cannot revoke folder B's tokens.
func (s *Store) RevokeRouteToken(jid, ownerFolder string) (bool, error) {
	res, err := s.db.Exec(
		`DELETE FROM route_tokens WHERE jid = ? AND owner_folder = ?`,
		jid, ownerFolder,
	)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}
