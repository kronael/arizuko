package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
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

// --- the archive lane (spec 5/8 "Secret and token values") ----------------
//
// The archive carries a route token's VALUE, which the config manifest never
// does, so the three functions below are this table's second reader and they
// live beside its first. `secrets` already had its pair here
// (ExportSecretRows / ValidateAndImportSecrets); route_tokens', invites' and
// onboarding's sat in resreg instead, which made a generic engine the owner of
// four tables it does not know anything else about.

// ArchiveRouteTokenRow is one row of the archive's route_tokens document:
// RouteTokensRow's shape (resreg/resources/route_tokens.go — jid,
// owner_folder, created_at, context) plus TokenHash, the verifier the
// config manifest never carries.
// Restricted to kind='route' at read time — pairing tokens (kind='pair')
// are excluded entirely, the same RowFilter the config-manifest resource
// already applies (10-minute single-use credentials, no archival value,
// reviving one actively harmful).
type ArchiveRouteTokenRow struct {
	TokenHash    string `yaml:"token_hash"`
	JID          string `yaml:"jid"`
	OwnerFolder  string `yaml:"owner_folder"`
	TargetFolder string `yaml:"target_folder,omitempty"`
	CreatedAt    string `yaml:"created_at"`
	Context      string `yaml:"context,omitempty"`
}

// ExportRouteTokens reads every kind='route' row (jid delivery bearers —
// never kind='pair' pairing links) with its token_hash verifier, hex-encoded
// for YAML transport — which is exactly TokenRef, since token_hash is
// TokenRefBytes of the same digest.
func (s *Store) ExportRouteTokens(ctx context.Context) ([]ArchiveRouteTokenRow, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT token_hash, jid, owner_folder, created_at, COALESCE(context,''),
		        COALESCE(target_folder,'')
		 FROM route_tokens WHERE kind = 'route' ORDER BY jid`)
	if err != nil {
		return nil, fmt.Errorf("query route_tokens: %w", err)
	}
	defer rows.Close()
	var out []ArchiveRouteTokenRow
	for rows.Next() {
		var hashBytes []byte
		var r ArchiveRouteTokenRow
		if err := rows.Scan(&hashBytes, &r.JID, &r.OwnerFolder, &r.CreatedAt, &r.Context, &r.TargetFolder); err != nil {
			return nil, fmt.Errorf("scan route_token: %w", err)
		}
		r.TokenHash = hex.EncodeToString(hashBytes)
		out = append(out, r)
	}
	return out, rows.Err()
}

// ImportRouteTokens UPSERTs by token_hash (the PK) — a route token's row
// content never legitimately changes once minted, so INSERT OR IGNORE is
// equivalent to UPSERT here. Every imported row's kind is forced to
// 'route': this function never writes a pairing token (rows carrying one
// were never exported in the first place, but the write side stays
// explicit rather than trusting the archive's own claim).
//
// Performs NO gating — the off-by-default / --force / proven-empty-target
// policy (spec 5/8 "Secret and token values" (2)) is `archive apply`
// orchestration in cmd/arizuko, not this primitive's job.
func (s *Store) ImportRouteTokens(ctx context.Context, rows []ArchiveRouteTokenRow) (int, error) {
	if len(rows) == 0 {
		return 0, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback() //nolint:errcheck // the commit below is the success path
	for _, r := range rows {
		hashBytes, herr := hex.DecodeString(r.TokenHash)
		if herr != nil {
			return 0, fmt.Errorf("route_token %s: bad token_hash: %w", r.JID, herr)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT OR IGNORE INTO route_tokens (token_hash, jid, owner_folder, created_at, context, kind, target_folder)
			 VALUES (?, ?, ?, ?, ?, 'route', ?)`,
			hashBytes, r.JID, r.OwnerFolder, r.CreatedAt, archiveNullStr(r.Context), archiveNullStr(r.TargetFolder),
		); err != nil {
			return 0, fmt.Errorf("insert route_token %s: %w", r.JID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return len(rows), nil
}

// CountRouteTokens counts EVERY row (both kind='route' and kind='pair') —
// the proven-empty-target gate (spec 5/8 "Secret and token values" (2))
// wants a genuinely fresh table, not merely zero route-kind rows.
func (s *Store) CountRouteTokens(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM route_tokens").Scan(&n)
	return n, err
}

// archiveNullStr maps an empty string to a bind-time NULL, matching the
// nullable TEXT columns the archive's tables declare (route_tokens.context,
// messages.sender_name, …). It is the same rule nilIfEmptyString states for
// the resreg row types, spelled for a bind argument.
func archiveNullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}
