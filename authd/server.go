package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/authd/authstore"
	"github.com/kronael/arizuko/groupfolder"
	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/obs"
)

var errReuse = errors.New("refresh token reuse detected")

// Authd is the sole signer. It holds the active ES256 key in memory plus every
// key still inside its serving window, and persists keys/refresh state through
// authstore.
type Authd struct {
	// st is every question authd asks its store. obj is the same store one
	// level down, because audit.Init and audit.Query take the driver.
	st           *authstore.Store
	obj          objstore.Store
	accessTTL    time.Duration // short access JWT lifetime (~15m)
	refreshTTL   time.Duration // rotating refresh lifetime
	maxAccessTTL time.Duration // longest access TTL ever minted; bounds a retired key's serving window

	// grants, when wired, re-snapshots the bare sub's scope ceiling on every
	// refresh (spec 5/1); nil reuses the stored refresh-row scope.
	grants GrantsFetcher

	mu      sync.RWMutex
	active  *auth.SigningKey      // current signing key
	serving []authstore.StoredKey // all keys whose verify window is open (active + recently retired)
}

func newAuthd(obj objstore.Store, accessTTL, refreshTTL, maxAccessTTL time.Duration) (*Authd, error) {
	a := &Authd{st: authstore.New(obj), obj: obj, accessTTL: accessTTL, refreshTTL: refreshTTL, maxAccessTTL: maxAccessTTL}
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
	keys, err := a.st.SigningKeysWithMaterial()
	if err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.active = nil
	a.serving = a.serving[:0]
	cutoff := time.Now()
	for _, kr := range keys {
		if kr.Active {
			a.active = kr.Key
			a.serving = append(a.serving, kr)
			continue
		}
		if kr.RetiredAt != nil && cutoff.Before(kr.RetiredAt.Add(a.maxAccessTTL)) {
			a.serving = append(a.serving, kr)
		}
	}
	return nil
}

// Rotate generates a new active key, retiring the old one as of now (it keeps
// verifying within its TTL window); emergency revoke is RevokeAllNow. Retire
// and install run in ONE guarded batch, so two concurrent Rotate() calls elect
// one winner; the loser gets authstore.ErrRotationLost.
func (a *Authd) Rotate() error {
	k, err := auth.NewSigningKey(genKid(time.Now()))
	if err != nil {
		return err
	}
	if err := a.st.RotateActiveKey(k, time.Now()); err != nil {
		return err
	}
	return a.reload()
}

// genKid returns the kid scheme "<created-unix>-<8 hex rand>": sortable by
// creation time, so a verifier can derive a key's creation order from the kid
// alone.
func genKid(created time.Time) string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%d-%s", created.Unix(), hex.EncodeToString(b))
}

// RevokeAllNow backdates the active key's retired_at past the serving window,
// closing verification of all currently-issued tokens immediately, then mints a
// fresh active key. This is the emergency-revoke path.
func (a *Authd) RevokeAllNow() error {
	if err := a.st.RetireActiveKeys(time.Now().Add(-a.maxAccessTTL - time.Second)); err != nil {
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
		out[kr.Key.Kid] = &kr.Key.Priv.PublicKey
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
		if kr.RetiredAt != nil {
			out[kr.Key.Kid] = *kr.RetiredAt
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
// the target's grants snapshot. sub is passed already prefixed ("user:123",
// "service:timed"); typ is the JWT `typ` claim.
func (a *Authd) MintForSubject(sub, typ string, requested, targetGrants []string, aud string) (string, error) {
	k, err := a.activeKey()
	if err != nil {
		return "", err
	}
	tok, err := k.MintForSubject(targetGrants, auth.TokenClaims{Sub: sub, Typ: typ, Scope: requested, Aud: aud}, a.accessTTL)
	if err == nil {
		obs.RecordTokenMint("access")
	}
	return tok, err
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

// signMinted signs c and returns the echo-back values. ttl<=0 falls back to
// accessTTL, and any ttl is clamped to maxAccessTTL; this is the ONE mint
// site. Access tokens verify offline against no revocation list, so an
// uncapped ttl is an unbounded revocation hole.
func (a *Authd) signMinted(c auth.TokenClaims, folder string, ttl time.Duration) (minted, error) {
	k, err := a.activeKey()
	if err != nil {
		return minted{}, err
	}
	if ttl <= 0 {
		ttl = a.accessTTL
	}
	if max := a.maxAccessTTL; max > 0 && ttl > max {
		ttl = max
	}
	c.Jti = auth.NewJTI()
	c.Extra = folderExtra(folder)
	tok, err := k.Sign(c, ttl)
	if err != nil {
		return minted{}, err
	}
	obs.RecordTokenMint("access")
	return minted{token: tok, jti: c.Jti, expiresAt: time.Now().Add(ttl)}, nil
}

// Downscope mints a "downscoped" token for the SAME sub as the parent:
// requested scope ⊆ parent scope, folder ⊆ parent folder, ttl ≤ parent
// remaining. parent_jti links it to the parent (spec 5/1 § POST /v1/tokens).
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
// scope bounded by the TARGET sub's grants snapshot, NOT the caller's scope
// (spec 5/1 § POST /v1/tokens).
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
	return groupfolder.Contains(parent, child)
}

// IssueRefresh starts a new refresh-token family for sub.
func (a *Authd) IssueRefresh(sub string, scope []string, aud string) (string, error) {
	return a.st.StartRefreshFamily(sub, scope, aud, a.refreshTTL)
}

// Refresh rotates a refresh token: verifies it is live and unused, mints a new
// access + successor refresh, and tombstones the old one. Presenting an
// already-used token is reuse: the whole family is revoked and errReuse
// returned. ctx bounds the grants re-snapshot.
func (a *Authd) Refresh(ctx context.Context, raw string) (access, newRefresh string, err error) {
	r, ok := a.st.RefreshTokenBySecret(raw)
	if !ok {
		return "", "", auth.ErrInvalidToken
	}
	if r.Revoked {
		return "", "", auth.ErrInvalidToken
	}
	if r.Used {
		// Reuse of a spent token: revoke the lineage. A failure here leaves a
		// known-stolen token's family live, so it is logged loud; the caller
		// still gets errReuse.
		if _, err := a.st.RevokeFamily(r.Family); err != nil {
			slog.Error("refresh-token family NOT revoked after reuse detection; a stolen token may still mint",
				"family", r.Family, "sub", r.Sub, "err", err)
		}
		return "", "", errReuse
	}
	if time.Now().After(r.Expires) {
		return "", "", auth.ErrExpiredToken
	}
	// Re-snapshot grants on refresh so a revoked user does not keep scope up
	// to the refresh TTL; a backend outage fails CLOSED. This runs BEFORE the
	// claim (BUGS F36): the claim and the successor's write share one batch,
	// and a remote HTTP call cannot sit inside it. A grants outage therefore
	// does not burn the presented token.
	scope := r.Scope
	if a.grants != nil {
		// r.Sub is ALREADY the bare canonical sub, and for an OAuth sub the
		// canonical form IS "google:<id>", so re-stripping at ":" would query
		// the wrong principal. Query r.Sub as-is.
		snap, gerr := a.grants.FetchGrants(ctx, r.Sub)
		switch {
		case gerr == nil:
			scope = snap.Scope
		case errors.Is(gerr, ErrNoGrants):
			scope = nil
		default:
			return "", "", errGrantsUnavailable
		}
	}
	// Compare-and-set plus the successor's write in ONE batch: only the caller
	// that sets used_at wins the right to rotate, and no family kill can land
	// between its win and its write. A lost race is reuse: revoke the family
	// and fail.
	newRefresh, won, err := a.st.ClaimAndRotateRefresh(raw, r.Family, r.Sub, scope, r.Aud, a.refreshTTL)
	if err != nil {
		return "", "", err
	}
	if !won {
		if _, err := a.st.RevokeFamily(r.Family); err != nil {
			slog.Error("refresh-token family NOT revoked after lost rotation race; a stolen token may still mint",
				"family", r.Family, "sub", r.Sub, "err", err)
		}
		return "", "", errReuse
	}
	// r.Sub is the BARE canonical sub (spec 5/1 "sub prefix rule"); the user:
	// prefix is added ONLY here at mint, so the access claim stays "user:<sub>".
	access, err = a.MintForSubject("user:"+r.Sub, "user", scope, scope, r.Aud)
	if err != nil {
		return "", "", err
	}
	return access, newRefresh, nil
}
