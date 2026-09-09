package store

// Identity pairing (spec 5/31): redeeming a kind='pair' route token writes the
// acl_membership edge that makes an anonymous channel identity resolve to a
// verified account. A channel identity holds no grants ever; the edge is the
// only bridge, and the only way to write one is for the account owner to
// authenticate and consent.
//
// A PAIRING CREDENTIAL IS A ROUTE TOKEN, so this file writes route_tokens' items
// through route_tokens.go's own helpers — routeTokenPut, routeTokenItem,
// routeTokenKey and the attrTok* names. One coordinate, one attribute set: a
// second spelling here would mint credentials at a coordinate LookupRouteToken's
// kind check never reaches, and a pairing link would resolve as nothing.
//
// `kind` IS WHAT KEEPS THE TWO APART. A delivery bearer resolves only through
// RouteTokenKindRoute and a pairing link only through RouteTokenKindPair, so
// neither credential can be redeemed as the other.
//
// THE TOKEN AND THE EDGE COMMIT TOGETHER, and that is the whole reason pairing
// is a kind of route token rather than a table of its own: both live in the same
// database, so the redemption spends the credential and writes the edge in ONE
// transaction the caller owns. What that gives up against AddMembership is the
// marker's compare-and-swap on the cycle walk; the engine's write lock is the
// serialisation instead (store/membership.go PutMembershipTx).

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
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

// IssuePairingLink mints a pairing token for jid and returns the raw token
// ONCE — only sha256(raw) is stored. It is the single pairing minter: the agent
// path (routd's issue_pairing_link) and the greeting path (onbod's poll tick)
// both come here, so the credential cannot drift apart in hashing or storage.
//
// The write rides in the CALLER'S transaction when it has one — routd mints
// inside resreg.invoke's mutation tx so the token lands with that call's audit
// row — and in a batch of its own when tx is nil, which is what onbod's greeting
// holds.
//
// ownerFolder is resolved entirely by the CALLER — routd binds it to the folder
// the JID already routes to, onbod passes "" because a greeting is sent before
// any human is known and there is no folder to name. Empty writes NO
// owner_folder attribute, which is the only null this model has. Neither
// PeekPairing nor RedeemPairing reads it, and pair-kind rows are already
// excluded from listing and revocation, so its absence costs nothing.
func (s *Store) IssuePairingLink(ctx context.Context, tx *sql.Tx, jid, ownerFolder string) (string, error) {
	if jid == "" {
		return "", fmt.Errorf("pairing: jid required")
	}
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("pairing: generate token: %w", err)
	}
	raw := hex.EncodeToString(b[:])
	w, err := routeTokenPut(raw, RouteToken{JID: jid, OwnerFolder: ownerFolder}, RouteTokenKindPair)
	if err != nil {
		return "", fmt.Errorf("pairing: mint token for %s: %w", jid, err)
	}
	if tx != nil {
		_, err = s.WriteEntitiesInTx(ctx, tx, nil, w)
	} else {
		_, err = s.WriteEntities(ctx, nil, w)
	}
	if err != nil {
		return "", fmt.Errorf("pairing: mint token for %s: %w", jid, err)
	}
	return raw, nil
}

// PeekPairing resolves a live pairing token to the channel JID it binds WITHOUT
// consuming it. GET on the pairing URL must be side-effect-free: chat platforms
// unfurl links, and an unfurl bot must not spend a pairing.
func (s *Store) PeekPairing(rawToken string) (string, error) {
	_, jid, err := s.pairingItem(context.Background(), rawToken)
	return jid, err
}

// pairingItem resolves a raw token to its item and the JID it binds, kind-scoped
// to 'pair' and bounded by PairingTTL. A route-kind token never resolves here,
// just as a pairing token never resolves through LookupRouteToken.
//
// The read is an exact key, so it touches one item.
func (s *Store) pairingItem(ctx context.Context, rawToken string) (objstore.Item, string, error) {
	if rawToken == "" {
		return objstore.Item{}, "", ErrPairingUnavailable
	}
	it, ok, err := routeTokenItem(ctx, s.Obj(), rawToken, RouteTokenKindPair)
	if err != nil {
		return objstore.Item{}, "", fmt.Errorf("resolve pairing token: %w", err)
	}
	if !ok {
		return objstore.Item{}, "", ErrPairingUnavailable
	}
	ts, err := time.Parse(core.TSLayout, it.Attrs[attrTokCreatedAt])
	if err != nil {
		return objstore.Item{}, "", fmt.Errorf("parse pairing created_at %q: %w",
			it.Attrs[attrTokCreatedAt], err)
	}
	if time.Since(ts) > PairingTTL {
		return objstore.Item{}, "", ErrPairingUnavailable
	}
	return it, it.Attrs[attrTokJID], nil
}

// RedeemPairing binds the channel identity named by rawToken to parentSub and
// consumes the token, in one transaction: resolve + age check, reject a
// different non-role parent, insert the edge, delete the token. Returns the
// paired JID. The new authority is live on the identity's next tool call —
// Authorize reads the DB live (spec 5/32 § No caching).
//
// The consume is CONDITIONAL on the token still being there, so two redeemers
// racing on one link cannot both win: the loser's whole transaction rolls back,
// edge included, and it is told the same ErrPairingUnavailable an expired link
// gets. Single use is the credential's own rule, not a comment.
func (s *Store) RedeemPairing(rawToken, parentSub string) (string, error) {
	if parentSub == "" {
		return "", ErrPairingUnavailable
	}
	ctx := context.Background()
	it, jid, err := s.pairingItem(ctx, rawToken)
	if err != nil {
		return "", err
	}

	// One parent per channel identity. Role parents are excluded: acl_membership
	// legitimately holds multi-parent rows for role membership, and a JID may
	// hold a pairing edge and a role membership at once (4e831f10). The rule is
	// about what pairing may write, so it is enforced here and not by a table
	// constraint. The child LEADS the coordinate, so this is a begins_with on
	// `child + KeySep` and never a scan.
	parents, err := MembershipParents(s.Obj())(ctx, jid)
	if err != nil {
		return "", fmt.Errorf("read existing parent of %s: %w", jid, err)
	}
	for _, p := range parents {
		if strings.HasPrefix(p, "role:") {
			continue
		}
		if p != parentSub {
			return "", ErrPairingConflict
		}
	}

	tx, err := s.db.Begin()
	if err != nil {
		return "", err
	}
	// commit already succeeded on the happy path; rollback is best-effort
	defer tx.Rollback() //nolint:errcheck

	if err := s.PutMembershipTx(ctx, tx, jid, parentSub, PairingAddedBy); err != nil {
		return "", err
	}
	spent, err := s.WriteEntitiesInTx(ctx, tx, nil, objstore.Write{
		Op: objstore.Delete, Entity: RouteTokens, Key: it.Key,
		Cond: objstore.Cond{Item: objstore.ItemPresent},
	})
	if err != nil {
		return "", fmt.Errorf("consume pairing token: %w", err)
	}
	if !spent {
		return "", ErrPairingUnavailable
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return jid, nil
}

// UnpairTx deletes ONE acl_membership edge on tx, scoped to added_by =
// PairingAddedBy so it can never reach role membership or a manifest-applied
// row. Returns whether an edge went. Takes effect on the identity's next tool
// call — Authorize reads the DB live (spec 5/32 § No caching).
//
// `added_by` is an attribute rather than part of the coordinate, so it is the
// write's CONDITION: the delete names one item and commits only when that item
// is pairing's, which is what the third term of the WHERE clause said. A refused
// batch is the "no such edge" answer.
func (s *Store) UnpairTx(ctx context.Context, tx *sql.Tx, child, parent string) (bool, error) {
	key, err := membershipKey(child, parent)
	if err != nil {
		return false, err
	}
	return s.WriteEntitiesInTx(ctx, tx, nil, objstore.Write{
		Op: objstore.Delete, Entity: ACLMembership, Key: key,
		Cond: objstore.Cond{Where: []objstore.Cmp{
			{Attr: attrMemAddedBy, Op: objstore.Eq, Value: PairingAddedBy},
		}},
	})
}
