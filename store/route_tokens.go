package store

import (
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/kronael/arizuko/audit"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/groupfolder"
)

// RouteToken maps a bearer token to a single inbound JID + admin folder (spec 5/W).
// JID kind is encoded in the prefix: `web:<folder>[/<suffix>]` for anonymous web chat,
// `hook:<folder>/<source>[...]` for labeled webhook ingest. owner_folder bounds
// revocation and may diverge from the folder embedded in the JID.
type RouteToken struct {
	JID         string
	OwnerFolder string
	CreatedAt   time.Time
	// Context is the optional issuer-authored per-link processing
	// instructions (spec 5/W § link context). Empty = none.
	Context string
	// TargetFolder is the folder the token DELIVERS to, recorded by the mint.
	// Empty for a token minted before the column existed; Folder falls back to
	// the JID grammar for those.
	TargetFolder string
}

// Folder returns the folder the token delivers to. The mint records the target
// (spec 5/W); the JID grammar answers only for a token minted before the
// target_folder column, because a suffixed JID is ambiguous — `web:t/sfx` reads
// as folder `t/sfx`, and `hook:a/b/c` reads as folder `a/b`.
func (t RouteToken) Folder() string {
	if t.TargetFolder != "" {
		return t.TargetFolder
	}
	return groupfolder.JidFolder(t.JID)
}

// ErrRouteTokenTargetTaken reports a mint whose JID already delivers to a
// different folder. A JID names ONE chat, so two folders must never mint the
// same JID: resolution reads target_folder back by JID and could not choose
// between the two answers.
var ErrRouteTokenTargetTaken = errors.New("jid already delivers to a different folder")

// RouteTokenQuerier is the *sql.DB / *sql.Tx subset the mint guard reads. Both
// mint paths (store.InsertRouteToken, routd.issueRouteTokenTx) call the guard
// through it, so one function holds the rule.
type RouteTokenQuerier interface {
	QueryRow(query string, args ...any) *sql.Row
}

// CheckRouteTokenTarget rejects a mint that would bind an existing JID to a
// second delivery folder. A row minted before the target_folder column records
// no target, so it never conflicts.
func CheckRouteTokenTarget(q RouteTokenQuerier, jid, target string) error {
	var other string
	err := q.QueryRow(
		`SELECT target_folder FROM route_tokens
		 WHERE jid = ? AND kind = ? AND target_folder IS NOT NULL AND target_folder <> ?
		 LIMIT 1`,
		jid, RouteTokenKindRoute, target).Scan(&other)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return fmt.Errorf("%w: %s delivers to %s", ErrRouteTokenTargetTaken, jid, other)
}

// RouteTokenTargetByJID returns the folder a web:/hook: JID delivers to, or "" when no
// token records one. Callers fall back to groupfolder.JidFolder for "" — the rule those
// rows were minted under.
//
// This is the single reader every folder-from-JID path goes through, so the grammar's
// ambiguity is answered in one place. CheckRouteTokenTarget keeps a JID bound to ONE
// folder, so LIMIT 1 is the whole answer.
func RouteTokenTargetByJID(q RouteTokenQuerier, jid string) (string, error) {
	var target string
	err := q.QueryRow(
		`SELECT target_folder FROM route_tokens
		 WHERE jid = ? AND kind = ? AND target_folder IS NOT NULL LIMIT 1`,
		jid, RouteTokenKindRoute).Scan(&target)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return target, nil
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
//
// Every mint MUST set TargetFolder — it is the folder the token delivers to,
// and only the mint knows it. An empty TargetFolder stores NULL, the shape of a
// row minted before the column, which reads back through the ambiguous JID
// grammar (spec 5/W § Resolution).
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
	if err := CheckRouteTokenTarget(s.db, t.JID, t.TargetFolder); err != nil {
		return err
	}
	ts := t.CreatedAt
	if ts.IsZero() {
		ts = time.Now()
	}
	actor, actorSub, surface := s.auditIdentity()
	return s.runAudited(func(tx *sql.Tx) (audit.Event, error) {
		_, err := tx.Exec(
			`INSERT INTO route_tokens (token_hash, jid, owner_folder, created_at, context, kind, target_folder) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			TokenRefBytes(rawToken), t.JID, t.OwnerFolder, core.TS(ts), nilIfEmpty(t.Context), RouteTokenKindRoute, nilIfEmpty(t.TargetFolder),
		)
		return audit.Event{
			Category: audit.CategoryChannel,
			Action:   "route_token.mint",
			Actor:    actor,
			ActorSub: actorSub,
			Surface:  surface,
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
		`SELECT jid, owner_folder, created_at, COALESCE(context,''), COALESCE(target_folder,'') FROM route_tokens WHERE token_hash = ? AND kind = ?`,
		TokenRefBytes(rawToken), RouteTokenKindRoute,
	).Scan(&t.JID, &t.OwnerFolder, &createdAt, &t.Context, &t.TargetFolder)
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
		`SELECT jid, owner_folder, created_at, COALESCE(context,''), COALESCE(target_folder,'') FROM route_tokens WHERE owner_folder = ? AND kind = ? ORDER BY created_at DESC`,
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
		if rows.Scan(&t.JID, &t.OwnerFolder, &createdAt, &t.Context, &t.TargetFolder) != nil {
			continue
		}
		t.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
		out = append(out, t)
	}
	return out
}

// RouteTokenOwner returns the owner_folder of the DELIVERY token with this jid.
//
// The owner is a COLUMN on the row, not something to parse back out of the JID:
// `token issue chat <folder> <suffix>` mints `web:<folder>/<suffix>`, so a JID
// parse yields `<folder>/<suffix>`, which is not a folder and matches no row
// (BUGS L52). Callers that already hold the owner from a grant keep passing it;
// the operator CLI reads it here.
func (s *Store) RouteTokenOwner(jid string) (string, bool) {
	var owner string
	err := s.db.QueryRow(
		`SELECT owner_folder FROM route_tokens WHERE jid = ? AND kind = ?`,
		jid, RouteTokenKindRoute,
	).Scan(&owner)
	if err != nil {
		return "", false
	}
	return owner, true
}

// RevokeRouteToken deletes DELIVERY tokens with the given jid owned by
// ownerFolder.
// Returns (true, nil) if at least one row was deleted, (false, nil) when
// no matching row existed. ACL scope: the caller's `owner_folder` MUST
// match — agents in folder A cannot revoke folder B's tokens.
func (s *Store) RevokeRouteToken(jid, ownerFolder string) (bool, error) {
	var hit bool
	actor, actorSub, surface := s.auditIdentity()
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
			Actor:    actor,
			ActorSub: actorSub,
			Surface:  surface,
			Resource: "route_tokens/" + jid,
			Folder:   ownerFolder,
			Outcome:  audit.OutcomeOK,
		}, nil
	})
	return hit, err
}
