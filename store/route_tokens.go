package store

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	"github.com/kronael/arizuko/audit"
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
	// Context is the optional issuer-authored per-link processing
	// instructions (spec 5/W § link context). Empty = none.
	Context string
}

// The JID-derived kind of a route token: which inbound surface its JID names.
// Distinct from the stored `kind` column below — that one says whether the row
// is a delivery bearer at all.
const (
	RouteTokenJIDKindChat = "chat" // jid prefix "web:"
	RouteTokenJIDKindHook = "hook" // jid prefix "hook:"
)

// The stored `kind` column (spec 5/31). Route delivery resolves only
// KindRoute rows and pairing redemption only KindPair rows, so neither
// credential can be redeemed as the other.
const (
	RouteTokenKindRoute = "route"
	RouteTokenKindPair  = "pair"
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

// HashRouteToken returns the sha256 hash that is persisted for a raw route
// token (the raw token is never stored). Exported so FS-mounted writers like
// dashd hash with the identical scheme instead of duplicating it.
func HashRouteToken(raw string) []byte {
	sum := sha256.Sum256([]byte(raw))
	return sum[:]
}

// RouteTokenJIDKind returns "chat" for web: jids, "hook" for hook: jids, ""
// otherwise.
func RouteTokenJIDKind(jid string) string {
	switch {
	case strings.HasPrefix(jid, "web:"):
		return RouteTokenJIDKindChat
	case strings.HasPrefix(jid, "hook:"):
		return RouteTokenJIDKindHook
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
	if RouteTokenJIDKind(t.JID) == "" {
		return fmt.Errorf("jid must start with web: or hook:")
	}
	ts := t.CreatedAt
	if ts.IsZero() {
		ts = time.Now()
	}
	return s.runAudited(func(tx *sql.Tx) (audit.Event, error) {
		_, err := tx.Exec(
			`INSERT INTO route_tokens (token_hash, jid, owner_folder, created_at, context, kind) VALUES (?, ?, ?, ?, ?, ?)`,
			HashRouteToken(rawToken), t.JID, t.OwnerFolder, ts.Format(time.RFC3339Nano), nilIfEmpty(t.Context), RouteTokenKindRoute,
		)
		return audit.Event{
			Category: audit.CategoryChannel,
			Action:   "route_token.mint",
			Actor:    "system",
			Surface:  audit.SurfaceGateway,
			Resource: "route_tokens/" + t.JID,
			Folder:   t.OwnerFolder,
			Outcome:  audit.OutcomeOK,
			ParamsSummary: map[string]any{
				"owner_folder": t.OwnerFolder,
				"jid_kind":     RouteTokenJIDKind(t.JID),
			},
		}, err
	})
}

// LookupRouteToken resolves a raw DELIVERY bearer token to its row. Returns
// (zero, false) when the token is unknown or is not a route-kind row — a
// pairing token (spec 5/31) never resolves here.
func (s *Store) LookupRouteToken(rawToken string) (RouteToken, bool) {
	if rawToken == "" {
		return RouteToken{}, false
	}
	var t RouteToken
	var createdAt string
	err := s.db.QueryRow(
		`SELECT jid, owner_folder, created_at, COALESCE(context,'') FROM route_tokens WHERE token_hash = ? AND kind = ?`,
		HashRouteToken(rawToken), RouteTokenKindRoute,
	).Scan(&t.JID, &t.OwnerFolder, &createdAt, &t.Context)
	if err != nil {
		return RouteToken{}, false
	}
	t.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	return t, true
}

// ListRouteTokens returns the DELIVERY tokens owned by ownerFolder. Pairing
// rows are excluded: enumerating ten-minute bearer attempts is surface nobody
// needs (spec 5/31 § Unpair).
func (s *Store) ListRouteTokens(ownerFolder string) []RouteToken {
	rows, err := s.db.Query(
		`SELECT jid, owner_folder, created_at, COALESCE(context,'') FROM route_tokens WHERE owner_folder = ? AND kind = ? ORDER BY created_at DESC`,
		ownerFolder, RouteTokenKindRoute,
	)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []RouteToken
	for rows.Next() {
		var t RouteToken
		var createdAt string
		if rows.Scan(&t.JID, &t.OwnerFolder, &createdAt, &t.Context) != nil {
			continue
		}
		t.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
		out = append(out, t)
	}
	return out
}

// RevokeRouteToken deletes DELIVERY tokens with the given jid owned by
// ownerFolder.
// Returns (true, nil) if at least one row was deleted, (false, nil) when
// no matching row existed. ACL scope: the caller's `owner_folder` MUST
// match — agents in folder A cannot revoke folder B's tokens.
func (s *Store) RevokeRouteToken(jid, ownerFolder string) (bool, error) {
	var hit bool
	err := s.runAudited(func(tx *sql.Tx) (audit.Event, error) {
		res, err := tx.Exec(
			`DELETE FROM route_tokens WHERE jid = ? AND owner_folder = ? AND kind = ?`,
			jid, ownerFolder, RouteTokenKindRoute,
		)
		if err != nil {
			return audit.Event{}, err
		}
		n, _ := res.RowsAffected()
		hit = n > 0
		return audit.Event{
			Category: audit.CategoryChannel,
			Action:   "route_token.revoke",
			Actor:    "system",
			Surface:  audit.SurfaceGateway,
			Resource: "route_tokens/" + jid,
			Folder:   ownerFolder,
			Outcome:  audit.OutcomeOK,
		}, nil
	})
	return hit, err
}
