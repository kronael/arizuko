package store

// Identity pairing (spec 5/31): redeeming a kind='pair' route token writes the
// acl_membership edge that makes an anonymous channel identity resolve to a
// verified account. A channel identity holds no grants ever; the edge is the
// only bridge, and the only way to write one is for the account owner to
// authenticate and consent.
//
// The token and the edge live in the same database, so both writes are ONE
// transaction — the reason pairing is a kind of route token rather than a table
// of its own.

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// PairingTTL bounds redemption, measured from the token's created_at. Short,
// but it is not the defence — a minter can mint again on demand. Consent at the
// browser step is (spec 5/31 § Consent is the security boundary).
const PairingTTL = 10 * time.Minute

// PairingAddedBy stamps acl_membership.added_by on every edge pairing writes.
// Unpair is scoped to it so it can never reach role membership.
const PairingAddedBy = "pairing"

// ErrPairingUnavailable is the ONE error for a missing, expired, consumed or
// malformed pairing token. Callers must not let a redeemer tell those apart.
var ErrPairingUnavailable = errors.New("pairing: link unavailable")

// ErrPairingConflict: the channel identity already has a different non-role
// parent. The only distinct error, because it is the only one the user can act
// on — expandPrincipals unions, so a second parent would silently hand the
// identity the union of two humans' authority.
var ErrPairingConflict = errors.New("pairing: channel identity is linked to another account")

// rowQuerier is satisfied by both *sql.DB and *sql.Tx so the token resolution
// has one implementation across the side-effect-free peek and the redeem tx.
type rowQuerier interface {
	QueryRow(query string, args ...any) *sql.Row
}

// PeekPairing resolves a live pairing token to the channel JID it binds WITHOUT
// consuming it. GET on the pairing URL must be side-effect-free: chat platforms
// unfurl links, and an unfurl bot must not spend a pairing.
func (s *Store) PeekPairing(rawToken string) (string, error) {
	return pairingJID(s.db, rawToken)
}

// RedeemPairing binds the channel identity named by rawToken to parentSub and
// consumes the token, in one transaction: resolve + age check, reject a
// different non-role parent, insert the edge, delete the token. Returns the
// paired JID. The new authority is live on the identity's next tool call —
// Authorize reads the DB live (spec 5/32 § No caching).
func (s *Store) RedeemPairing(rawToken, parentSub string) (string, error) {
	if parentSub == "" {
		return "", ErrPairingUnavailable
	}
	tx, err := s.db.Begin()
	if err != nil {
		return "", err
	}
	// commit already succeeded on the happy path; rollback is best-effort
	defer tx.Rollback() //nolint:errcheck

	jid, err := pairingJID(tx, rawToken)
	if err != nil {
		return "", err
	}

	// One parent per channel identity. Role parents are excluded: acl_membership
	// legitimately holds multi-parent rows for role membership, and a JID may
	// hold a pairing edge and a role membership at once (4e831f10). The rule is
	// about what pairing may write, so it is enforced here and not by a table
	// constraint.
	var existing string
	switch err := tx.QueryRow(
		`SELECT parent FROM acl_membership WHERE child = ? AND parent NOT LIKE 'role:%'`,
		jid,
	).Scan(&existing); {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return "", fmt.Errorf("read existing parent of %s: %w", jid, err)
	case existing != parentSub:
		return "", ErrPairingConflict
	}

	if err := putMembershipTx(tx, jid, parentSub, PairingAddedBy); err != nil {
		return "", err
	}
	if _, err := tx.Exec(
		`DELETE FROM route_tokens WHERE token_hash = ?`, HashRouteToken(rawToken),
	); err != nil {
		return "", fmt.Errorf("consume pairing token: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return jid, nil
}

// pairingJID resolves a raw token to its JID, kind-scoped to 'pair' and bounded
// by PairingTTL. A route-kind token never resolves here, just as a pairing
// token never resolves through LookupRouteToken.
func pairingJID(q rowQuerier, rawToken string) (string, error) {
	if rawToken == "" {
		return "", ErrPairingUnavailable
	}
	var jid, createdAt string
	err := q.QueryRow(
		`SELECT jid, created_at FROM route_tokens WHERE token_hash = ? AND kind = ?`,
		HashRouteToken(rawToken), RouteTokenKindPair,
	).Scan(&jid, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrPairingUnavailable
	}
	if err != nil {
		return "", fmt.Errorf("resolve pairing token: %w", err)
	}
	ts, err := time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return "", fmt.Errorf("parse pairing created_at %q: %w", createdAt, err)
	}
	if time.Since(ts) > PairingTTL {
		return "", ErrPairingUnavailable
	}
	return jid, nil
}
