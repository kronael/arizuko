package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/authd/authstore"
	"github.com/kronael/arizuko/groupfolder"
	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/obs"
)

var errReuse = errors.New("refresh token reuse detected")

// Authd is the sole signer. It holds no key: each mint, each verification and
// each JWKS answer reads the signing keys from authstore, so a rotation or a
// retirement another process wrote applies to the next request.
type Authd struct {
	// st is every question authd asks its store. obj is the same store one
	// level down, because audit.Init and audit.Query take the driver.
	st           *authstore.Store
	obj          objstore.Store
	accessTTL    time.Duration // short access JWT lifetime (~15m)
	refreshTTL   time.Duration // rotating refresh lifetime
	maxAccessTTL time.Duration // longest access TTL ever minted; bounds a retired key's serving window

	// grants, when wired, re-snapshots the bare sub's scope ceiling on every
	// refresh (authd/README.md); nil reuses the stored refresh-row scope.
	grants GrantsFetcher
}

func newAuthd(obj objstore.Store, accessTTL, refreshTTL, maxAccessTTL time.Duration) (*Authd, error) {
	a := &Authd{st: authstore.New(obj), obj: obj, accessTTL: accessTTL, refreshTTL: refreshTTL, maxAccessTTL: maxAccessTTL}
	active, _, err := a.readKeys()
	if err != nil {
		return nil, err
	}
	if active == nil {
		if err := a.Rotate(); err != nil {
			return nil, err
		}
	}
	return a, nil
}

// readKeys reads the signing keys from the store and answers the active key,
// nil when the slot is empty, and every key that serves now: the active key and
// each retired key with now < retired_at + maxAccessTTL.
func (a *Authd) readKeys() (*auth.SigningKey, []authstore.StoredKey, error) {
	stored, err := a.st.SigningKeysWithMaterial()
	if err != nil {
		return nil, nil, err
	}
	var active *auth.SigningKey
	var serving []authstore.StoredKey
	now := time.Now()
	for _, kr := range stored {
		if kr.Active {
			active = kr.Key
			serving = append(serving, kr)
			continue
		}
		if kr.RetiredAt != nil && now.Before(kr.RetiredAt.Add(a.maxAccessTTL)) {
			serving = append(serving, kr)
		}
	}
	return active, serving, nil
}

// Rotate generates a new active key, retiring the old one as of now (it keeps
// verifying within its TTL window). Retire and install run in ONE guarded
// batch, so two concurrent Rotate() calls elect one winner; the loser gets
// authstore.ErrRotationLost.
func (a *Authd) Rotate() error {
	k, err := auth.NewSigningKey(genKid(time.Now()))
	if err != nil {
		return err
	}
	return a.st.RotateActiveKey(k, time.Now())
}

// genKid returns the kid scheme "<created-unix>-<8 hex rand>": sortable by
// creation time, so a verifier can derive a key's creation order from the kid
// alone.
func genKid(created time.Time) string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%d-%s", created.Unix(), hex.EncodeToString(b))
}

// PublicKeys returns the public halves of all serving keys, keyed by kid.
func (a *Authd) PublicKeys() (map[string]*ecdsa.PublicKey, error) {
	_, serving, err := a.readKeys()
	if err != nil {
		return nil, err
	}
	out := map[string]*ecdsa.PublicKey{}
	for _, kr := range serving {
		out[kr.Key.Kid] = &kr.Key.Priv.PublicKey
	}
	return out, nil
}

// LocalKeySet is the in-process verifier authd uses on itself: public keys plus
// per-kid retirement, so a token minted with a stolen retired key (iat past
// retired_at) is rejected even inside the key's serving window. The active key
// carries no retirement and so no iat cap.
func (a *Authd) LocalKeySet() (*auth.KeySet, error) {
	_, serving, err := a.readKeys()
	if err != nil {
		return nil, err
	}
	pub := map[string]*ecdsa.PublicKey{}
	retired := map[string]time.Time{}
	for _, kr := range serving {
		pub[kr.Key.Kid] = &kr.Key.Priv.PublicKey
		if kr.RetiredAt != nil {
			retired[kr.Key.Kid] = *kr.RetiredAt
		}
	}
	return auth.NewKeySetWithRetirement(pub, retired), nil
}

func (a *Authd) activeKey() (*auth.SigningKey, error) {
	active, _, err := a.readKeys()
	if err != nil {
		return nil, err
	}
	if active == nil {
		return nil, auth.ErrNoSigningKey
	}
	return active, nil
}

// MintForSubject signs an authoritative access token for sub, scope bounded by
// the target's grants snapshot. sub is passed already prefixed ("user:123",
// "service:routd"); typ is the JWT `typ` claim.
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
// claim (root/operator tokens omit it — authd/README.md claim set).
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
// remaining. parent_jti links it to the parent (authd/README.md /v1/tokens).
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
	rem := time.Until(parent.Expires)
	if rem <= 0 {
		return minted{}, auth.ErrExpiredToken
	}
	if ttl <= 0 || ttl > rem {
		ttl = rem
	}
	return a.signMinted(auth.TokenClaims{
		Sub: parent.Sub, Typ: "downscoped", Scope: requested, Aud: parent.Aud, ParentJTI: parent.JTI,
	}, folder, ttl)
}

// IssuerMint mints a fresh user/service token for a (possibly different) sub,
// scope bounded by the TARGET sub's grants snapshot, NOT the caller's scope
// (authd/README.md /v1/tokens).
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
	// r.Sub is the BARE canonical sub (authd/README.md); the user:
	// prefix is added ONLY here at mint, so the access claim stays "user:<sub>".
	access, err = a.MintForSubject("user:"+r.Sub, "user", scope, scope, r.Aud)
	if err != nil {
		return "", "", err
	}
	return access, newRefresh, nil
}
