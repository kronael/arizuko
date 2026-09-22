package store

// Identity pairing (spec 5/31-identity-pairing): redeeming a kind='pair' route token writes the
// acl_membership edge that makes an anonymous channel identity resolve to a
// verified account. A channel identity holds no grants ever; the edge is the
// only bridge, and the only way to write one is for the account owner to
// authenticate and consent.
//
// A pairing credential IS a route token, written through route_tokens.go's own
// helpers: a second spelling would mint credentials at a coordinate
// LookupRouteToken's kind check never reaches. `kind` keeps the two apart, so
// neither credential can be redeemed as the other. The token and the edge
// commit in ONE guarded batch, the cycle walk's marker compare-and-swap
// included (membership.go AddEdgeWith).

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
)

// PairingTTL bounds redemption, measured from the token's created_at. Short,
// but it is not the defence: a minter can mint again on demand. Consent at the
// browser step is (spec 5/31-identity-pairing § Consent is the security boundary).
const PairingTTL = 10 * time.Minute

// PairingAddedBy stamps acl_membership.added_by on every edge pairing writes.
// Unpair is scoped to it so it can never reach role membership.
const PairingAddedBy = "pairing"

// ErrPairingUnavailable is the ONE error for a missing, expired, consumed or
// malformed pairing token. Callers must not let a redeemer tell those apart.
var ErrPairingUnavailable = errors.New("pairing: link unavailable")

// ErrPairingConflict: the channel identity already has a different non-role
// parent. The only distinct error, because it is the only one the user can act
// on; expandPrincipals unions, so a second parent would silently hand the
// identity the union of two humans' authority.
var ErrPairingConflict = errors.New("pairing: channel identity is linked to another account")

// IssuePairingLink mints a pairing token for jid and returns the raw token
// ONCE; only sha256(raw) is stored. It is onbod's greeting path.
//
// ownerFolder is the CALLER's: routd binds it to the folder the JID already
// routes to, onbod passes "" because a greeting is sent before any human is
// known. Empty writes NO owner_folder attribute, and nothing reads it.
func (s *Store) IssuePairingLink(ctx context.Context, jid, ownerFolder string) (string, error) {
	raw, w, err := pairingMint(jid, ownerFolder)
	if err != nil {
		return "", err
	}
	if err := s.WriteEntities(ctx, nil, w); err != nil {
		return "", fmt.Errorf("pairing: mint token for %s: %w", jid, err)
	}
	return raw, nil
}

// IssuePairingLinkInto mints a pairing token and buffers its write on the
// caller's set: routd's issue_pairing_link, whose token lands in the batch
// that carries that call's audit row.
func (s *Store) IssuePairingLinkInto(ws *WriteSet, jid, ownerFolder string) (string, error) {
	raw, w, err := pairingMint(jid, ownerFolder)
	if err != nil {
		return "", err
	}
	ws.Add(w)
	return raw, nil
}

// pairingMint generates the bearer and renders the write that stores it: the
// single pairing minter for the agent path and the greeting path. The RAW
// token is answered before anything is written and never read back.
func pairingMint(jid, ownerFolder string) (string, objstore.Write, error) {
	if jid == "" {
		return "", objstore.Write{}, fmt.Errorf("pairing: jid required")
	}
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", objstore.Write{}, fmt.Errorf("pairing: generate token: %w", err)
	}
	raw := hex.EncodeToString(b[:])
	w, err := routeTokenPut(raw, RouteToken{JID: jid, OwnerFolder: ownerFolder}, RouteTokenKindPair)
	if err != nil {
		return "", objstore.Write{}, fmt.Errorf("pairing: mint token for %s: %w", jid, err)
	}
	return raw, w, nil
}

// PeekPairing resolves a live pairing token to the channel JID it binds WITHOUT
// consuming it. GET on the pairing URL must be side-effect-free: chat platforms
// unfurl links, and an unfurl bot must not spend a pairing.
func (s *Store) PeekPairing(rawToken string) (string, error) {
	_, jid, err := s.pairingItem(context.Background(), rawToken)
	return jid, err
}

// pairingItem resolves a raw token to its item and the JID it binds,
// kind-scoped to 'pair' and bounded by PairingTTL. An exact key read.
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
// consumes the token, in one batch: resolve + age check, reject a different
// non-role parent, insert the edge, delete the token. Returns the paired JID.
// The new authority is live on the identity's next tool call (spec 5/32-acl-unified § No
// caching).
//
// The consume is CONDITIONAL on the token still being there, so two redeemers
// racing on one link cannot both win: the loser's whole batch is refused, edge
// included, and it gets the same ErrPairingUnavailable an expired link gets.
func (s *Store) RedeemPairing(rawToken, parentSub string) (string, error) {
	if parentSub == "" {
		return "", ErrPairingUnavailable
	}
	ctx := context.Background()
	it, jid, err := s.pairingItem(ctx, rawToken)
	if err != nil {
		return "", err
	}

	// One parent per channel identity. Role parents are excluded: a JID may
	// hold a pairing edge and a role membership at once (4e831f10). The rule
	// is about what pairing may write, so it is enforced here and not by a
	// table constraint.
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

	// THE EDGE AND THE TOKEN GO IN ONE BATCH, under the cycle guard. The token
	// delete is the only guarded write in it, so a false is the token being
	// spent between the read above and this write (BUGS L256).
	spent, err := s.AddEdgeWith(ctx, jid, parentSub, PairingAddedBy, objstore.Write{
		Op: objstore.Delete, Entity: RouteTokens, Key: it.Key,
		Cond: objstore.Cond{Item: objstore.ItemPresent},
	})
	if err != nil {
		return "", fmt.Errorf("consume pairing token: %w", err)
	}
	if !spent {
		return "", ErrPairingUnavailable
	}
	return jid, nil
}

// UnpairInto buffers the deletion of ONE acl_membership edge on the caller's
// write set, scoped to added_by = PairingAddedBy so it can never reach role
// membership or a manifest-applied row. Takes effect on the identity's next
// tool call (spec 5/32-acl-unified § No caching). `added_by` is an attribute rather than
// part of the coordinate, so it is the write's CONDITION; refused is the "no
// such edge" that refusal means.
func (s *Store) UnpairInto(ws *WriteSet, child, parent string, refused error) error {
	key, err := membershipKey(child, parent)
	if err != nil {
		return err
	}
	return ws.AddGuarded(refused, objstore.Write{
		Op: objstore.Delete, Entity: ACLMembership, Key: key,
		Cond: objstore.Cond{Where: []objstore.Cmp{
			{Attr: attrMemAddedBy, Op: objstore.Eq, Value: PairingAddedBy},
		}},
	})
}
