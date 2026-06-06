package main

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/store"
)

// maxBodyBytes caps request bodies on the token endpoints. Both carry only a
// short secret/token; a larger body is malformed or hostile, so we refuse to
// buffer it (memory/CPU DoS guard — specs/5/1 unbounded-body finding).
const maxBodyBytes = 4 << 10

// serviceGrants maps a service principal to the scopes it may obtain at boot.
// Daemon-initiated work carries one of these (specs/5/1 "Service identity").
// Declared here, mechanical — no per-deployment DB state.
var serviceGrants = map[string][]string{
	// authd self-mints service:authd to call the grants backend at boot/login/
	// refresh (spec 5/1 § Login-time scope snapshot: "scope grants:read"). It
	// needs grants:read, not keys:read — /v1/keys is public.
	"service:authd": {"grants:read"},
	"service:timed": {"messages:write", "tasks:read"},
	"service:onbod": {"messages:write", "groups:write"},
	"service:gated": {"messages:write", "messages:read", "tasks:read", "tasks:write"},
	// service:routd dispatches runs (runs:run → runed) and resolves identities
	// at authd's identity endpoint (identity:read → GET /v1/identities/{sub}).
	"service:routd": {"runs:run", "identity:read"},
}

// GrantsFetcher resolves the scope ceiling for an issuer-mint target. authd is
// not the grants authority (gated is — spec 5/1 § Login-time scope snapshot);
// the gated cutover step supplies an HTTP-backed implementation. Until then
// it is nil and issuer-mint fails closed (503 grants_unavailable).
type GrantsFetcher interface {
	// FetchGrants returns the BARE sub's scope ceiling + folder subtree.
	// ErrNoGrants → the sub has no grant rows; any other error → backend down.
	FetchGrants(ctx context.Context, bareSub string) (GrantsSnapshot, error)
}

type GrantsSnapshot struct {
	Scope  []string
	Folder string
}

// ErrNoGrants distinguishes "sub has no grants" from "grants backend down" so
// the issuer-mint handler can map each intentionally.
var ErrNoGrants = errors.New("no grants for sub")

type server struct {
	a              *Authd
	serviceSecrets map[string]string // bootstrap key -> service principal (sub)
	grants         GrantsFetcher     // nil until the gated cutover step wires it
	secureCookies  bool              // mark refresh cookies Secure (https deployment)
}

func (s *server) mux() *http.ServeMux {
	m := http.NewServeMux()
	// /v1/keys is public and mounts before any auth — backends fetch it to
	// verify offline.
	m.HandleFunc("GET /v1/keys", s.handleKeys)
	m.HandleFunc("POST /v1/tokens", s.handleTokens)
	m.HandleFunc("POST /v1/service-token", s.handleServiceToken)
	m.HandleFunc("POST /v1/refresh", s.handleRefresh)
	m.HandleFunc("GET /v1/identities/{sub}", s.handleIdentity)
	m.HandleFunc("GET /health", s.handleHealth)
	return m
}

func (s *server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	if err := s.a.db.Ping(); err != nil {
		http.Error(w, "db unreachable", http.StatusServiceUnavailable)
		return
	}
	_, _ = w.Write([]byte("ok"))
}

func (s *server) handleKeys(w http.ResponseWriter, _ *http.Request) {
	body, err := auth.PublicJWKS(s.servingKeys()...)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	_, _ = w.Write(body)
}

func (s *server) servingKeys() []*auth.SigningKey {
	s.a.mu.RLock()
	defer s.a.mu.RUnlock()
	out := make([]*auth.SigningKey, 0, len(s.a.serving))
	for _, kr := range s.a.serving {
		out = append(out, kr.key)
	}
	return out
}

// handleServiceToken exchanges a daemon's bootstrap secret for a short service
// JWT whose sub is that daemon's principal and whose scope is the principal's
// declared grants. The leaked secret buys only that one daemon's scoped token —
// never a signing capability.
//
// The secret rides the Authorization header (kept out of body-logging — spec
// 5/1 §435); the body carries only the daemon name. The presented secret is
// matched by a constant-time SHA-256 compare against the configured secrets, so
// neither a wrong daemon name nor a wrong secret leaks timing.
func (s *server) handleServiceToken(w http.ResponseWriter, r *http.Request) {
	secret := bearer(r)
	var req struct {
		Daemon string `json:"daemon"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Daemon == "" || secret == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	principal, ok := s.matchServiceSecret(req.Daemon, secret)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "bad_service_key", "unknown daemon or hash mismatch")
		return
	}
	token, err := s.a.MintForSubject(principal, "service", nil, serviceGrants[principal], "")
	if err != nil {
		slog.Error("mint service token", "principal", principal, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{
		"token":      token,
		"token_type": "Bearer",
		"scope":      serviceGrants[principal],
	})
}

// matchServiceSecret resolves (daemon, presented secret) to a service principal
// using a constant-time SHA-256 hash compare. The daemon name selects the
// expected principal ("service:<daemon>"); the secret must hash-match the one
// configured for it. Returns the principal on success.
func (s *server) matchServiceSecret(daemon, secret string) (string, bool) {
	want := "service:" + daemon
	presented := sha256.Sum256([]byte(secret))
	matched := false
	principal := ""
	// Walk every configured secret so timing does not reveal which daemon names
	// are known; the daemon-name check is folded into the constant-time result.
	for key, prin := range s.serviceSecrets {
		stored := sha256.Sum256([]byte(key))
		hashEq := subtle.ConstantTimeCompare(presented[:], stored[:]) == 1
		nameEq := subtle.ConstantTimeCompare([]byte(prin), []byte(want)) == 1
		if hashEq && nameEq {
			matched = true
			principal = prin
		}
	}
	return principal, matched
}

// handleTokens is POST /v1/tokens: issuer-mint OR downscope, picked by the
// requested typ + whether the (verified) caller holds tokens:mint. Both modes
// require a valid bearer; the body declares the request (spec 5/1 § POST
// /v1/tokens).
func (s *server) handleTokens(w http.ResponseWriter, r *http.Request) {
	caller, err := auth.VerifyHTTP(r, s.a.LocalKeySet())
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "unauthorized", "valid bearer required")
		return
	}
	var req struct {
		Typ     string   `json:"typ"`
		Sub     string   `json:"sub"`
		Scope   []string `json:"scope"`
		Folder  string   `json:"folder"`
		Aud     string   `json:"audience"`
		TTLSecs int      `json:"ttl_seconds"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	for _, sc := range req.Scope {
		if sc == "*:*" || sc == "*" {
			writeErr(w, http.StatusBadRequest, "invalid_scope", "global *:* not allowed")
			return
		}
	}
	ttl := time.Duration(req.TTLSecs) * time.Second

	// Mode selection. An explicit typ="downscoped" is always a downscope (the
	// runed broker declares it). Otherwise issuer-mint when the caller holds
	// tokens:mint and targets a DIFFERENT sub; everything else downscopes.
	// Downscope forces the minted sub to the caller's (spec 5/1: "the sub field
	// is forced to the caller's") — a service principal cannot mint a token
	// bearing an arbitrary user's sub via this path (no impersonation).
	issuer := req.Typ != "downscoped" &&
		auth.HasScope(caller.Scope, "tokens", "mint") && req.Sub != "" && req.Sub != caller.Sub
	var m minted
	if issuer {
		m, err = s.issuerMint(r.Context(), req.Sub, req.Scope, req.Aud, ttl)
	} else {
		m, err = s.a.Downscope(caller, req.Scope, req.Folder, ttl)
	}
	switch {
	case err == nil:
	case errors.Is(err, auth.ErrScopeTooBroad) && issuer:
		writeErr(w, http.StatusForbidden, "scope_exceeds_minter", "requested scope exceeds target grants")
		return
	case errors.Is(err, auth.ErrScopeTooBroad):
		writeErr(w, http.StatusForbidden, "scope_exceeds_parent", "requested scope exceeds parent token")
		return
	case errors.Is(err, errGrantsUnavailable):
		writeErr(w, http.StatusServiceUnavailable, "grants_unavailable", "grants backend unavailable")
		return
	case errors.Is(err, ErrNoGrants):
		writeErr(w, http.StatusForbidden, "scope_exceeds_minter", "target sub has no grants")
		return
	default:
		slog.Error("mint token", "issuer", issuer, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{
		"token":      m.token,
		"jti":        m.jti,
		"expires_at": m.expiresAt.UTC().Format(time.RFC3339),
	})
}

var errGrantsUnavailable = errors.New("grants backend unavailable")

// issuerMint fetches the target's grants snapshot and mints against it. With no
// grants fetcher wired (the additive-step default) it fails closed — authd
// cannot evaluate the required ceiling, so it must not mint (spec 5/1: issuer
// mint is bounded by the target's grants, never the caller's scope).
func (s *server) issuerMint(ctx context.Context, sub string, requested []string, aud string, ttl time.Duration) (minted, error) {
	// Invites mint USER tokens only — never service (spec 5/1 § POST /v1/tokens:
	// "An invite mints user, never service. Delegation, never escalation").
	// typ is forced here, ignoring the caller's body, to close the
	// privilege-escalation hole where a caller asks for typ="service".
	const typ = "user"
	if s.grants == nil {
		return minted{}, errGrantsUnavailable
	}
	snap, err := s.grants.FetchGrants(ctx, bareSub(sub))
	if err != nil {
		if errors.Is(err, ErrNoGrants) {
			return minted{}, ErrNoGrants
		}
		return minted{}, errGrantsUnavailable
	}
	return s.a.IssuerMint(sub, typ, requested, snap.Scope, snap.Folder, aud, ttl)
}

// bareSub strips the "user:"/"service:" prefix for the grants lookup (spec 5/1
// "sub prefix rule" — bare sub everywhere except the JWT sub claim).
func bareSub(sub string) string {
	if i := strings.IndexByte(sub, ':'); i >= 0 {
		return sub[i+1:]
	}
	return sub
}

// bearer extracts the token from an Authorization: Bearer header ("" if absent).
func bearer(r *http.Request) string {
	hdr := r.Header.Get("Authorization")
	if !strings.HasPrefix(hdr, "Bearer ") {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(hdr, "Bearer "))
}

// writeErr emits the spec 5/1 JSON error shape with the matching HTTP status.
func writeErr(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": code, "message": msg})
}

// handleRefresh rotates a refresh token, returning a new access JWT plus the
// successor refresh token by the SAME channel it was presented on (spec 5/1
// § POST /v1/refresh): cookie in → successor in Set-Cookie, omitted from the
// JSON body (stays HttpOnly); body in → successor in the JSON body, no cookie.
// A request with both a cookie and a body token uses the cookie (browser wins).
// Reuse of a spent token revokes the family and returns 401.
func (s *server) handleRefresh(w http.ResponseWriter, r *http.Request) {
	raw, browser := refreshFromRequest(w, r)
	if raw == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	access, newRefresh, err := s.a.Refresh(r.Context(), raw)
	if err != nil {
		if err == errReuse {
			slog.Warn("refresh token reuse — family revoked")
		}
		if errors.Is(err, errGrantsUnavailable) {
			writeErr(w, http.StatusServiceUnavailable, "grants_unavailable", "grants backend unavailable")
			return
		}
		writeErr(w, http.StatusUnauthorized, "invalid_refresh", "missing, expired, or reused refresh token")
		return
	}
	exp := time.Now().Add(s.a.accessTTL).UTC().Format(time.RFC3339)
	body := map[string]any{"token": access, "expires_at": exp}
	if browser {
		http.SetCookie(w, &http.Cookie{
			Name: "refresh_token", Value: newRefresh, Path: "/",
			Expires: time.Now().Add(s.a.refreshTTL), HttpOnly: true,
			Secure: s.secureCookies, SameSite: http.SameSiteLaxMode,
		})
	} else {
		body["refresh_token"] = newRefresh
	}
	writeJSON(w, body)
}

// refreshFromRequest pulls the refresh token off the cookie (browser channel,
// preferred) or the JSON body. browser reports which channel won — it decides
// where the successor is delivered (Set-Cookie vs body).
func refreshFromRequest(w http.ResponseWriter, r *http.Request) (raw string, browser bool) {
	if c, err := r.Cookie("refresh_token"); err == nil && c.Value != "" {
		return c.Value, true
	}
	var req struct {
		RefreshToken string `json:"refresh_token"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return "", false
	}
	return req.RefreshToken, false
}

// handleIdentity resolves a platform sender sub (e.g. tg:123, discord:abc) to
// its canonical identity and the full set of subs that identity claims, reading
// authd's OWN auth.db identities/identity_claims (authd owns identity — spec
// 5/9). routd's inspect_identity tool snapshots this over HTTP instead of
// sibling-reading gated's messages.db. Bearer-gated by identity:read
// (service:routd carries it). 200 {"identity":{...}|null,"subs":[...]}; an
// unclaimed sub returns {"identity":null,"subs":[]} (200, not 404) so the tool
// renders the unclaimed shape directly.
func (s *server) handleIdentity(w http.ResponseWriter, r *http.Request) {
	caller, err := auth.VerifyHTTP(r, s.a.LocalKeySet())
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "unauthorized", "valid bearer required")
		return
	}
	if !auth.HasScope(caller.Scope, "identity", "read") {
		writeErr(w, http.StatusForbidden, "forbidden", "missing scope identity:read")
		return
	}
	sub := r.PathValue("sub")
	if sub == "" {
		writeErr(w, http.StatusBadRequest, "bad_request", "sub required")
		return
	}
	idn, subs, ok := store.New(s.a.db).GetIdentityForSub(sub)
	if !ok {
		writeJSON(w, map[string]any{"identity": nil, "subs": []string{}})
		return
	}
	writeJSON(w, map[string]any{
		"identity": map[string]any{
			"id":         idn.ID,
			"name":       idn.Name,
			"created_at": idn.CreatedAt.Format(time.RFC3339),
		},
		"subs": subs,
	})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
