package main

import (
	"crypto/ecdsa"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"sync"
	"time"

	"github.com/google/uuid"
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
func (a *Authd) Rotate() error {
	k, err := auth.NewSigningKey("k-" + uuid.NewString())
	if err != nil {
		return err
	}
	if err := retireActiveKeys(a.db, time.Now()); err != nil {
		return err
	}
	if err := insertKey(a.db, k); err != nil {
		return err
	}
	return a.reload()
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
// (e.g. "user:123", "service:timed"); sub is passed already prefixed.
func (a *Authd) MintForSubject(sub string, requested, targetGrants []string, aud string) (string, error) {
	k, err := a.activeKey()
	if err != nil {
		return "", err
	}
	return k.MintForSubject(targetGrants, auth.TokenClaims{Sub: sub, Scope: requested, Aud: aud}, a.accessTTL)
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
	if err := markRefreshUsed(a.db, raw); err != nil {
		return "", "", err
	}
	newRefresh, err = rotateRefresh(a.db, r.family, r.sub, r.scope, r.aud, a.refreshTTL)
	if err != nil {
		return "", "", err
	}
	access, err = a.MintForSubject(r.sub, r.scope, r.scope, r.aud)
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
