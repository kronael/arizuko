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
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/obs"
	"github.com/kronael/arizuko/resreg"
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
	"service:timed": {"messages:write", "tasks:read", "tasks:write"},
	"service:onbod": {"messages:write", "groups:write"},
	// service:routd dispatches runs (runs:run → runed), resolves identities at
	// authd's identity endpoint (identity:read → GET /v1/identities/{sub}), and
	// federates the /invite + /gate slash commands to onbod (invites:write +
	// gates:write → onbod's /v1/invites + /v1/gates; onbod OWNS those tables).
	"service:routd": {"runs:run", "identity:read", "invites:write", "gates:write"},
	// service:runed holds no grant: runed mints nothing and only VERIFIES its
	// callers against authd's JWKs. The broker ceiling that used to sit here died
	// with the per-spawn capability token (BUGS B1/L8) — the turn is credentialed by
	// routd's SO_PEERCRED socket.
	// Channel adapters post inbound to routd's /v1/messages. Each exchanges its
	// AUTHD_SERVICE_KEY for a service:<adapter> JWT (spec 5/1); a missing entry means
	// empty scope and every inbound 401/403s. Multi-account variants share the base
	// principal.
	"service:teled":  {"messages:write"},
	"service:whapd":  {"messages:write"},
	"service:discd":  {"messages:write"},
	"service:slakd":  {"messages:write"},
	"service:bskyd":  {"messages:write"},
	"service:reditd": {"messages:write"},
	"service:emaid":  {"messages:write"},
	"service:twitd":  {"messages:write"},
	"service:linkd":  {"messages:write"},
	"service:gitd":   {"messages:write"},
	// webd is web ingress, not a channel adapter, but it posts the same way:
	// route-token /chat + /hook submissions land in routd's /v1/messages. Without
	// this entry every form/widget submission 403s ("router unavailable" 502 to
	// the user) — the strengths form was dead because webd had no scope.
	"service:webd": {"messages:write"},
	// dashd is the operator UI. It proxies rather than touching other daemons'
	// tables, and a missing entry means empty scope, so the control 403s silently
	// (BUGS F15a). The eleven scopes and what each buys:
	//
	//   - runs:kill — POST /v1/runs/stop, the /dash/runed/ kill button.
	//   - routes:read/write — /dash/engagement/'s view and its force-disengage
	//     control. Both are a subset of reach dashd already holds through its FS
	//     mount on routd.db; what they buy is moving a page off the direct-DB read.
	//     routes:read's widest read (ListRouteTokens) never selects the token value.
	//   - audit:read — /dash/audit/ federating routd's, runed's and authd's trails
	//     (spec 5/I). NOT a subset: dashd is mounted on neither runed.db nor
	//     auth.db, so this is new authority on the token authority. What bounds it
	//     is that audit.Query names audit_log and no other table, and authd's DSN is
	//     redacted at the writer and scrubbed from history (authd migration 0007).
	//   - signing_keys:read — SigningKeysRow. priv_pem and pub_pem are in no SELECT
	//     list, no Row struct and no db: tag, and the query is a constant. It reads
	//     the rotation, never the key.
	//   - sessions:read — SessionsRow carries no credential column: authd persists
	//     only a sha256, and token_hash is unnamed in the projection.
	//   - sessions:write — exactly DELETE /v1/sessions/{family_id}, one UPDATE
	//     setting revoked_at. A kill verb on the token authority, taken because the
	//     audit row lands inside the mutation's own transaction (a failed audit
	//     write rolls the revoke back) and 5/I made that row readable. A leaked
	//     dashd key can END sessions, never create or extend one; retiring the
	//     active key has no wire face at all (spec 5/1 § JWK rotation mechanics).
	//   - pending_actions:read/write — /dash/approvals/ (spec 5/19). The read shows
	//     the tool name and the arguments the agent chose; the write is one guarded
	//     UPDATE on a `held` row and cannot create a hold or delete a decision.
	//   - dead_letters:read/write — /dash/routd/'s dead-letter section (BUGS L23).
	//     Their own pair rather than messages:read/write, which is the point:
	//     messages:write is "speak as a channel", and requeueing one already-composed
	//     row is strictly smaller. The write is one guarded 'failed'→'pending'.
	//
	// None is reachable by a human bearer: a user token's scope list holds folder
	// globs and auth.scopeMatches rejects a held value without a colon. signing_keys
	// and sessions also REFUSE a folder-claimed caller outright (instanceWideGate) —
	// neither table has a folder column to narrow by.
	//
	// The ceiling is exactly these eleven. dashd does not originate work (runs:run),
	// speak as a channel (messages:write), or read credentials (secrets:read,
	// grants:read). authd/service_dashd_test.go pins both the set and the count.
	// tasks:read and messages:read are reads dashd ALREADY performed by another
	// route: its tasks, activity and usage pages ran arbitrary SQL against
	// routd.db. 6/28's read path moves them to GET /v1/tasks and routd's message
	// reads, so the scopes confer no new visibility — they REDUCE reach, from a
	// file handle to two endpoints the owner gates. Both read-only: the cockpit
	// mints and mutates nothing (BUGS L142).
	"service:dashd": {
		"runs:kill", "routes:read", "routes:write", "audit:read",
		"signing_keys:read", "sessions:read", "sessions:write",
		"pending_actions:read", "pending_actions:write",
		"dead_letters:read", "dead_letters:write",
		"tasks:read", "messages:read",
	},
}

// GrantsFetcher resolves the scope ceiling for an issuer-mint target. authd is
// not the grants authority (routd is — spec 5/1 § Login-time scope snapshot);
// GRANTS_URL unset → nil, and issuer-mint fails closed (503 grants_unavailable).
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
	grants         GrantsFetcher     // nil when GRANTS_URL is unset
	secureCookies  bool              // mark refresh cookies Secure (https deployment)
	// dashKeys gates GET /dash/authd/ (spec 6/28 auth layer 2): the dashboard
	// trusts the proxyd-verified end-user identity, not a bearer. authd verifies
	// its OWN tokens, so this is its local keyset; nil → local dev, gate open.
	dashKeys *auth.KeySet
}

// mux builds authd's COMPLETE served surface — including /openapi.json, /auth/*
// and /metrics, which main used to register afterwards. An /openapi.json mounted
// outside it forces every test to re-supply the resource list by hand (BUGS F40).
// cfg nil leaves /auth/* unmounted.
func (s *server) mux(cfg *core.Config) *http.ServeMux {
	m := http.NewServeMux()
	// /v1/keys is public and mounts before any auth — backends fetch it to
	// verify offline.
	m.HandleFunc("GET /v1/keys", s.handleKeys)
	m.HandleFunc("POST /v1/tokens", s.handleTokens)
	m.HandleFunc("POST /v1/service-token", s.handleServiceToken)
	m.HandleFunc("POST /v1/refresh", s.handleRefresh)
	m.HandleFunc("GET /v1/identities/{sub}", s.handleIdentity)
	m.HandleFunc("GET /health", s.handleHealth)
	s.mountAudit(m)
	s.mountSigningKeys(m)
	s.mountSessions(m)
	// The operator dashboard (spec 6/28): authd's own /dash/ namespace, gated
	// by the proxyd-stamped operator identity rather than a bearer.
	m.HandleFunc("GET /dash/authd/", s.handleDash)
	m.HandleFunc("POST /dash/authd/revoke", s.handleDashRevoke)
	// OAuth /auth/* (spec 5/1): authd is the OAuth provider, minting ES256.
	// Mounted only when provider config is present (AUTH_BASE_URL + a client id).
	if cfg != nil {
		s.secureCookies = strings.HasPrefix(auth.AuthBaseURL(cfg), "https://")
		s.registerOAuth(m, cfg)
	}
	m.HandleFunc("GET /openapi.json", resreg.OpenAPIHandler("authd", m))
	if obs.MetricsEnabled() {
		m.Handle("GET /metrics", obs.MetricsHandler())
	}
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
// declared grants. A leaked secret buys only that daemon's scoped token, never a
// signing capability. The secret rides the Authorization header, kept out of
// body-logging; the presented value is matched by a constant-time SHA-256
// compare, so neither a wrong daemon name nor a wrong secret leaks timing.
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
	if _, after, ok := strings.Cut(sub, ":"); ok {
		return after
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
	obs.RecordTokenRefresh(refreshOutcome(err))
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

// refreshOutcome maps a Refresh error to a bounded metric outcome label
// (spec 5/O arizuko_token_refreshes_total).
func refreshOutcome(err error) string {
	switch {
	case err == nil:
		return "success"
	case err == errReuse:
		return "revoked"
	case errors.Is(err, auth.ErrExpiredToken):
		return "expired"
	default:
		return "invalid"
	}
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

// handleIdentity resolves a sub to its canonical user and every sub that user has
// linked, reading auth.db's auth_users + oauth_identities. It previously read
// identities/identity_claims, which had this read surface but no writer, so the
// endpoint always answered "unclaimed" (BUGS P2). An unclaimed sub returns
// {"identity":null,"subs":[]} with 200, not 404, so the tool renders that shape.
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
	userID, name, createdAt, subs, ok := oauthIdentityForSub(s.a.db, sub)
	if !ok {
		writeJSON(w, map[string]any{"identity": nil, "subs": []string{}})
		return
	}
	writeJSON(w, map[string]any{
		"identity": map[string]any{
			"id":         userID,
			"name":       name,
			"created_at": createdAt,
		},
		"subs": subs,
	})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
