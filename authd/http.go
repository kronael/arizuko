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
	// service:runed is the broker ceiling for per-turn agent tokens: it downscopes
	// its own service:runed token to the agent's capabilities, so it must hold at
	// least that set (runed ManagerConfig.Scopes / routd RunScopes). Missing this
	// entry → empty ceiling → every agent turn 403s on downscope (scope_exceeds_parent).
	"service:runed": {"messages:send:own_group", "chats:read:own_group"},
	// Channel adapters post inbound to routd's /v1/messages (messages:write).
	// Each exchanges its AUTHD_SERVICE_KEY for a service:<adapter> JWT (spec 5/1);
	// missing the entry → empty scope → every inbound 401/403s (the split's A1).
	// Multi-account variants (`<adapter>-<label>`) share the base principal.
	"service:teled":  {"messages:write"},
	"service:whapd":  {"messages:write"},
	"service:discd":  {"messages:write"},
	"service:mastd":  {"messages:write"},
	"service:slakd":  {"messages:write"},
	"service:bskyd":  {"messages:write"},
	"service:reditd": {"messages:write"},
	"service:emaid":  {"messages:write"},
	"service:twitd":  {"messages:write"},
	"service:linkd":  {"messages:write"},
	// webd is web ingress, not a channel adapter, but it posts the same way:
	// route-token /chat + /hook submissions land in routd's /v1/messages. Without
	// this entry every form/widget submission 403s ("router unavailable" 502 to
	// the user) — the strengths form was dead because webd had no scope.
	"service:webd": {"messages:write"},
	// dashd is the operator UI. It proxies rather than touching other daemons'
	// tables: POST /v1/runs/stop (runed, gated runs:kill — server.go), the whapd
	// pair endpoints (no scope gate), and /v1/proxyd_routes (proxyd authorizes the
	// FORWARDED operator identity, not a service scope; dashd's bearer only proves
	// transit, see proxyd trustedForwarders).
	// Missing entry → empty scope → the kill button 403s; same shape as the four
	// cases above, and it shipped that way (BUGS F15a).
	//
	// routes:read is the READ half, added for /dash/engagement/ (spec 5/G item 6,
	// BUGS F31). It is not a widening of what dashd can SEE: dashd is FS-mounted on
	// routd.db (dash.dbRoutd, dashd/main.go) and already reads routes, groups and
	// route_tokens straight out of the table, so this scope is a strict subset of
	// reach it holds — what it buys is moving one page OFF the direct-DB read that
	// is its own recorded defect. Nor does it leak a secret: routes:read's widest
	// read is ListRouteTokens, which selects jid/owner_folder/created_at/context and
	// never the token value (store/route_tokens.go:137), and route_tokens/resolve is
	// a reverse lookup that needs the token already in hand.
	// routes:write is the WRITE half, added for /dash/engagement/'s force-disengage
	// control (spec 5/G item 6). It reaches exactly one thing dashd asks for —
	// POST /v1/engagement with ttl_seconds=0 — and the two conditions the read
	// half's sign-off named as owed are both met: routd writes the audit_log row
	// inside the mutation's own transaction (DB.SetEngagementAudited), and the
	// write path contains on the window's CLAIMING folder, the same predicate the
	// list read applies, so this scope can never reach a window the read half
	// could not already show. Everything else routes:write covers (routes,
	// web_routes, groups) dashd ALREADY writes directly through its FS mount on
	// routd.db, so like routes:read this is a subset of reach it holds, not new
	// authority — what it buys is one mutation moving OFF the direct-DB path.
	//
	// audit:read is the READ of every daemon's own audit_log, added so
	// /dash/audit/ can federate routd's, runed's and authd's trails through
	// their APIs (spec 5/I, BUGS F29). Before it, runed's run.hold/run.kill
	// rows and authd's login rows were reachable only with sqlite3 on the box:
	// an operator who killed a run could not see who did.
	//
	// This one is NOT a subset of reach dashd already holds — routes:read/write
	// were, because dashd is FS-mounted on routd.db, but it is mounted on
	// NEITHER runed.db NOR auth.db. It is therefore new authority on the token
	// authority itself, and it was audited as such rather than assumed:
	// authd's params_summary had exactly one writer (daemon.start's
	// {dsn, serving_keys, service_subs}), the two counts are len() values, the
	// DSN is now redacted at the writer (audit.redactRE) and scrubbed from
	// history (authd migration 0007), and no signing key, refresh token or
	// service secret is reachable — audit.Query selects from audit_log and
	// names no other table.
	//
	// The scope is unreachable by any human bearer, which is what makes it
	// operator-only rather than merely operator-intended: a user token's scope
	// list holds FOLDER GLOBS, and auth.scopeMatches rejects any held value
	// without a colon, so neither `acme/**` nor an operator's `**` satisfies
	// it. dashd is the only holder, and its /dash/audit/ page is
	// requireOperator-gated.
	//
	// signing_keys:read, sessions:read and sessions:write are the /dash/authd/
	// page (spec 5/1 DoD item 6). They are the three the spec named as
	// deliberately WITHHELD — "granting the token authority's kill verb to a
	// daemon with no caller for it is authority without a user" — so what
	// changed is that the caller exists, not that the reasoning softened. Each
	// is bounded by the resource it names, and the bound is a projection rather
	// than a promise:
	//
	//   - signing_keys:read reaches SigningKeysRow — kid, alg, active,
	//     created_at, retired_at. `priv_pem` and `pub_pem` appear in no SELECT
	//     list, no Row struct and no db: tag, and the query is a constant with
	//     no argument that could add a column, so this scope cannot be pointed
	//     at key material by any caller. It reads the ROTATION, never the key.
	//   - sessions:read reaches SessionsRow, which carries no credential column
	//     at all: authd persists only a sha256 of a refresh token, and
	//     token_hash is unnamed in the projection and in the query. The
	//     strongest fact this scope yields is that a login exists.
	//   - sessions:write reaches exactly DELETE /v1/sessions/{family_id}, one
	//     UPDATE that sets revoked_at. It cannot mint, cannot rotate a key, and
	//     cannot delete a row — the tombstone IS the reuse-detection evidence,
	//     so the record of a session survives its revocation.
	//
	// sessions:write is the sharpest of the three: it is a kill verb on the
	// token authority. It is taken on the two conditions the routes:write
	// sign-off established and BUGS F15a left open. The audit row lands inside
	// the mutation's OWN transaction — resreg.invoke opens it, the handler
	// revokes in it, the event is emitted into it, and a failed audit write
	// rolls the revoke back — so an unrecorded revoke is not expressible. And
	// the row is READABLE, because 5/I federated authd's audit_log into
	// /dash/audit/; F15a's objection was that a revoke recorded only in auth.db
	// is invisible on the page that exists to show it, which was true when it
	// was written and is not now.
	//
	// The blast radius is bounded the same way the reads are: a leaked dashd
	// key buys the ability to END sessions, never to create or extend one.
	// Signing stays authd's alone, and the fleet-wide lever — retiring the
	// active key — has no wire face at all, deliberately (spec 5/1 § JWK
	// rotation mechanics), so no scope reaches it and /dash/authd/ ships the
	// per-login control with the fleet-wide one stated as out-of-band.
	//
	// All three are unreachable by a human bearer for the same mechanical
	// reason audit:read is: a user token's scope list holds folder globs and
	// auth.scopeMatches rejects a held value without a colon. On top of that,
	// signing_keys and sessions REFUSE a folder-claimed caller outright
	// (instanceWideGate) — neither table has a folder column to narrow by, so
	// serving one everything would be the recorded cross-tenant list-all leak.
	//
	// pending_actions:read and pending_actions:write are the /dash/approvals/
	// review page (spec 5/19): the list of tool calls held for human approval
	// and the approve/reject verdict on one. Both are bounded by what the
	// endpoints reach. The read returns PendingActionsRow — the tool name and
	// the arguments the AGENT chose to send, which is exactly the material an
	// operator must see to review; no secret, token or key column exists in the
	// table. The write reaches one guarded UPDATE on a `held` row (routd
	// resolveHoldTx): it cannot create a hold, cannot delete the record of a
	// decision, and routd writes the audit row inside the verdict's own
	// transaction. Like audit:read, neither scope is reachable by a human
	// bearer (folder-glob scope lists carry no colon), and the chat path's
	// IsOperator gate is matched on this path by dashd's requireOperator page
	// gate.
	//
	// The ceiling is exactly these nine. dashd proxies and reads; it does not
	// originate work (runs:run), speak as a channel (messages:write), or read
	// credentials (secrets:read, grants:read). authd/service_dashd_test.go pins
	// both halves — the nine that must be here and the count, so a tenth
	// scope of any name fails there before it ships.
	"service:dashd": {
		"runs:kill", "routes:read", "routes:write", "audit:read",
		"signing_keys:read", "sessions:read", "sessions:write",
		"pending_actions:read", "pending_actions:write",
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
}

// mux builds authd's COMPLETE served surface — including /openapi.json,
// /auth/* and /metrics, which main used to register afterwards. A constructor
// that stops short of the real surface is the blind spot the doc-vs-mux guard
// exists to close: an /openapi.json mounted outside it forces every test to
// re-supply the resource list by hand (BUGS F40).
//
// cfg nil (config load failed, or a test with no provider) leaves /auth/*
// unmounted, exactly as the old `cerr == nil` branch did.
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

// handleIdentity resolves a sub to its canonical user and every sub that user
// has linked, reading auth.db's auth_users + oauth_identities — the model the
// OAuth login path actually populates. It previously read
// identities/identity_claims, which had this read surface but no writer, so the
// endpoint always answered "unclaimed" (BUGS P2). routd's inspect_identity tool
// snapshots this over HTTP.
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
