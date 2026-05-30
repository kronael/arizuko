package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/kronael/arizuko/auth"
)

// Authd is the sole signer. It holds the active ES256 key in memory plus every
// key still inside its serving window, and persists keys/refresh state to the
// shared DB.
type Authd struct {
	db           *sql.DB
	accessTTL    time.Duration // short access JWT lifetime (~15m)
	refreshTTL   time.Duration // rotating refresh lifetime
	maxAccessTTL time.Duration // longest access TTL ever minted; bounds a retired key's serving window

	// grants, when wired, re-snapshots the bare sub's scope ceiling on every
	// refresh (spec 5/1 § refresh: "re-runs the snapshot so a refreshed token
	// reflects current grants"). nil = reuse the stored refresh-row scope.
	grants GrantsFetcher

	mu      sync.RWMutex
	active  *auth.SigningKey // current signing key
	serving []keyRow         // all keys whose verify window is open (active + recently retired)
}

func newAuthd(db *sql.DB, accessTTL, refreshTTL, maxAccessTTL time.Duration) (*Authd, error) {
	a := &Authd{db: db, accessTTL: accessTTL, refreshTTL: refreshTTL, maxAccessTTL: maxAccessTTL}
	if err := a.reload(); err != nil {
		return nil, err
	}
	if a.active == nil {
		if err := a.Rotate(); err != nil {
			return nil, err
		}
	}
	return a, nil
}

// reload pulls keys from the DB and rebuilds the in-memory active + serving set
// using the time-based window (active OR now < retired_at + maxAccessTTL).
func (a *Authd) reload() error {
	keys, err := loadKeys(a.db)
	if err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.active = nil
	a.serving = a.serving[:0]
	cutoff := time.Now()
	for _, kr := range keys {
		if kr.active {
			a.active = kr.key
			a.serving = append(a.serving, kr)
			continue
		}
		if kr.retiredAt != nil && cutoff.Before(kr.retiredAt.Add(a.maxAccessTTL)) {
			a.serving = append(a.serving, kr)
		}
	}
	return nil
}

// Rotate generates a new active key, retiring the old one as of now (it keeps
// verifying within its TTL window). Emergency revoke is RevokeAllNow.
//
// Retire + insert run in ONE transaction, and the `idx_signing_keys_one_active`
// partial-unique index guards "exactly one active key": two concurrent
// Rotate() calls cannot both commit a second active row — the loser fails the
// constraint and rolls back, leaving a single active signer.
func (a *Authd) Rotate() error {
	k, err := auth.NewSigningKey(genKid(time.Now()))
	if err != nil {
		return err
	}
	if err := rotateActiveKey(a.db, k, time.Now()); err != nil {
		return err
	}
	return a.reload()
}

// genKid returns the spec kid scheme: "<created-unix>-<8 hex rand>", sortable
// by creation time and collision-resistant. Replaces the unsortable uuid so a
// verifier can derive a key's creation order from the kid alone.
func genKid(created time.Time) string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%d-%s", created.Unix(), hex.EncodeToString(b))
}

// RevokeAllNow backdates every active key's retired_at past the serving window,
// closing verification of all currently-issued tokens immediately, then mints a
// fresh active key. This is the emergency-revoke path.
func (a *Authd) RevokeAllNow() error {
	if err := retireActiveKeys(a.db, time.Now().Add(-a.maxAccessTTL-time.Second)); err != nil {
		return err
	}
	return a.Rotate()
}

// PublicKeys returns the public halves of all serving keys, keyed by kid — the
// in-process KeySet backing /v1/keys and local verification.
func (a *Authd) PublicKeys() map[string]*ecdsa.PublicKey {
	a.mu.RLock()
	defer a.mu.RUnlock()
	out := map[string]*ecdsa.PublicKey{}
	for _, kr := range a.serving {
		out[kr.key.Kid] = &kr.key.Priv.PublicKey
	}
	return out
}

// retiredKeys returns the retirement time per still-serving retired kid. The
// active key is absent (no iat cap). Backs the local KeySet's retired-key
// forgery check.
func (a *Authd) retiredKeys() map[string]time.Time {
	a.mu.RLock()
	defer a.mu.RUnlock()
	out := map[string]time.Time{}
	for _, kr := range a.serving {
		if kr.retiredAt != nil {
			out[kr.key.Kid] = *kr.retiredAt
		}
	}
	return out
}

// LocalKeySet is the in-process verifier authd uses on itself: public keys plus
// per-kid retirement, so a token minted with a stolen retired key (iat past
// retired_at) is rejected even inside the key's serving window.
func (a *Authd) LocalKeySet() *auth.KeySet {
	return auth.NewKeySetWithRetirement(a.PublicKeys(), a.retiredKeys())
}

func (a *Authd) activeKey() (*auth.SigningKey, error) {
	a.mu.RLock()
	k := a.active
	a.mu.RUnlock()
	if k == nil {
		return nil, auth.ErrNoSigningKey
	}
	return k, nil
}

// MintForSubject signs an authoritative access token for sub, scope bounded by
// the target's grants snapshot. The minted claim carries the prefixed subject
// (e.g. "user:123", "service:timed"); sub is passed already prefixed. typ is
// the JWT `typ` claim ("user" | "service").
func (a *Authd) MintForSubject(sub, typ string, requested, targetGrants []string, aud string) (string, error) {
	k, err := a.activeKey()
	if err != nil {
		return "", err
	}
	return k.MintForSubject(targetGrants, auth.TokenClaims{Sub: sub, Typ: typ, Scope: requested, Aud: aud}, a.accessTTL)
}

// MintNarrower signs a delegated token: requested must be a subset of the
// parent token's scope. Backs the mint_token MCP tool.
func (a *Authd) MintNarrower(sub string, parentScope, requested []string, aud string) (string, error) {
	k, err := a.activeKey()
	if err != nil {
		return "", err
	}
	return k.MintNarrower(parentScope, auth.TokenClaims{Sub: sub, Scope: requested, Aud: aud}, a.accessTTL)
}

// minted is one signed access token plus the values the HTTP layer echoes back.
type minted struct {
	token     string
	jti       string
	expiresAt time.Time
}

// folderExtra builds the optional arz/folder private claim. Empty folder = no
// claim (root/operator tokens omit it — spec 5/1 § JWT claim set).
func folderExtra(folder string) map[string]string {
	if folder == "" {
		return nil
	}
	return map[string]string{"arz/folder": folder}
}

// signMinted signs c (with a fresh jti, the given ttl, and an optional folder
// claim) and returns the echo-back values. ttl<=0 falls back to accessTTL.
func (a *Authd) signMinted(c auth.TokenClaims, folder string, ttl time.Duration) (minted, error) {
	k, err := a.activeKey()
	if err != nil {
		return minted{}, err
	}
	if ttl <= 0 {
		ttl = a.accessTTL
	}
	c.Jti = auth.NewJTI()
	c.Extra = folderExtra(folder)
	tok, err := k.Sign(c, ttl)
	if err != nil {
		return minted{}, err
	}
	return minted{token: tok, jti: c.Jti, expiresAt: time.Now().Add(ttl)}, nil
}

// Downscope mints a "downscoped" token for the SAME sub as the parent: requested
// scope must be ⊆ parent scope, folder ⊆ parent folder, ttl ≤ parent remaining.
// parent_jti links it to the parent (spec 5/1 § POST /v1/tokens downscope mode).
// runed/broker.go is the live caller.
func (a *Authd) Downscope(parent auth.Subject, requested []string, folder string, ttl time.Duration) (minted, error) {
	for _, want := range requested {
		if !auth.HasScopeCoveredBy(parent.Scope, want) {
			return minted{}, auth.ErrScopeTooBroad
		}
	}
	if folder != "" && !folderWithin(parentFolder(parent), folder) {
		return minted{}, auth.ErrScopeTooBroad
	}
	if len(requested) == 0 {
		requested = append([]string(nil), parent.Scope...)
	}
	if folder == "" {
		folder = parentFolder(parent)
	}
	if rem := time.Until(parent.Expires); rem > 0 && (ttl <= 0 || ttl > rem) {
		ttl = rem
	}
	return a.signMinted(auth.TokenClaims{
		Sub: parent.Sub, Typ: "downscoped", Scope: requested, Aud: parent.Aud, ParentJTI: parent.JTI,
	}, folder, ttl)
}

// IssuerMint mints a fresh user/service token for a (possibly different) sub,
// scope bounded by the TARGET sub's grants snapshot — NOT the caller's scope
// (spec 5/1 § POST /v1/tokens issuer mint). targetGrants/folder come from the
// grants snapshot the caller fetched.
func (a *Authd) IssuerMint(sub, typ string, requested, targetGrants []string, folder, aud string, ttl time.Duration) (minted, error) {
	for _, want := range requested {
		if !auth.HasScopeCoveredBy(targetGrants, want) {
			return minted{}, auth.ErrScopeTooBroad
		}
	}
	if len(requested) == 0 {
		requested = append([]string(nil), targetGrants...)
	}
	return a.signMinted(auth.TokenClaims{Sub: sub, Typ: typ, Scope: requested, Aud: aud}, folder, ttl)
}

// parentFolder reads the arz/folder claim a verified parent carries ("" = none).
func parentFolder(s auth.Subject) string { return s.Extra["arz/folder"] }

// folderWithin reports whether child is parent or a descendant subtree of it.
// Empty parent = unrestricted (no folder bound).
func folderWithin(parent, child string) bool {
	if parent == "" {
		return true
	}
	return child == parent || strings.HasPrefix(child, parent+"/")
}

// IssueRefresh starts a new refresh-token family for sub.
func (a *Authd) IssueRefresh(sub string, scope []string, aud string) (string, error) {
	return issueRefresh(a.db, sub, scope, aud, a.refreshTTL)
}

// Refresh rotates a refresh token: verifies it is live and unused, mints a new
// access + successor refresh, and tombstones the old one. Presenting an
// already-used token is reuse — the whole family is revoked and errReuse
// returned.
func (a *Authd) Refresh(raw string) (access, newRefresh string, err error) {
	r, ok := lookupRefresh(a.db, raw)
	if !ok {
		return "", "", auth.ErrInvalidToken
	}
	if r.revoked {
		return "", "", auth.ErrInvalidToken
	}
	if r.used {
		// Reuse of a spent token: revoke the lineage.
		_ = revokeFamily(a.db, r.family)
		return "", "", errReuse
	}
	if time.Now().After(r.expires) {
		return "", "", auth.ErrExpiredToken
	}
	// Atomic compare-and-set: only the goroutine that flips used_at NULL->now
	// wins the right to rotate. A lost race means another redeem of the SAME
	// token already won — concurrent redeem of a one-time token is reuse, so
	// revoke the family and fail, exactly as a sequential replay would.
	won, err := markRefreshUsed(a.db, raw)
	if err != nil {
		return "", "", err
	}
	if !won {
		_ = revokeFamily(a.db, r.family)
		return "", "", errReuse
	}
	// Re-snapshot grants on refresh so a revoked user does not keep scope up to
	// the refresh TTL (spec 5/1 § refresh). With no fetcher wired, the stored
	// scope is the ceiling; with one, the fresh snapshot is, and a backend
	// outage fails CLOSED (mint nothing) rather than reusing stale scope.
	scope := r.scope
	if a.grants != nil {
		snap, gerr := a.grants.FetchGrants(context.Background(), bareSub(r.sub))
		switch {
		case gerr == nil:
			scope = snap.Scope
		case errors.Is(gerr, ErrNoGrants):
			scope = nil
		default:
			return "", "", errGrantsUnavailable
		}
	}
	newRefresh, err = rotateRefresh(a.db, r.family, r.sub, scope, r.aud, a.refreshTTL)
	if err != nil {
		return "", "", err
	}
	access, err = a.MintForSubject(r.sub, "user", scope, scope, r.aud)
	if err != nil {
		return "", "", err
	}
	return access, newRefresh, nil
}

func genRefresh() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
