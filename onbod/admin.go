package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/store"
)

// admin serves onbod's bearer-gated /v1/* surface: the invite + gate writers
// that dashd, the host CLI, and routd's /invite + /gate commands reach instead
// of touching onbod's tables directly in the split (spec 5/5 § Daemon
// ownership). All mutations go through store.New(db) against onbod's OWNED DB
// (onbod.db in the split, messages.db in the monolith) so the same audited
// writers gated used apply verbatim.
type admin struct {
	db *sql.DB
	ks *auth.KeySet // authd JWKS; nil (AUTHD_URL unset / monolith) → open, like routd's nil verifier
}

// authed verifies the bearer token against authd's JWKS and checks the token
// carries one of anyScope. nil ks → open (local-dev / monolith). Mirrors routd's
// server.authed. Fails CLOSED: a verify error or missing scope is denied.
func (a *admin) authed(w http.ResponseWriter, r *http.Request, anyScope ...string) bool {
	if a.ks == nil {
		return true
	}
	sub, err := auth.VerifyHTTP(r, a.ks)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "unauthorized", err.Error())
		return false
	}
	for _, want := range anyScope {
		res, verb, ok := strings.Cut(want, ":")
		if ok && auth.HasScope(sub.Scope, res, verb) {
			return true
		}
	}
	writeErr(w, http.StatusForbidden, "forbidden", "missing scope "+strings.Join(anyScope, " or "))
	return false
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]string{"error": code, "message": msg})
}

// inviteJSON is the wire shape for an invite row (create response + list rows).
type inviteJSON struct {
	Token       string `json:"token"`
	TargetGlob  string `json:"target_glob"`
	IssuedBySub string `json:"issued_by_sub"`
	IssuedAt    string `json:"issued_at"`
	ExpiresAt   string `json:"expires_at,omitempty"`
	MaxUses     int    `json:"max_uses"`
	UsedCount   int    `json:"used_count"`
}

func toInviteJSON(inv store.Invite) inviteJSON {
	out := inviteJSON{
		Token: inv.Token, TargetGlob: inv.TargetGlob, IssuedBySub: inv.IssuedBySub,
		IssuedAt: inv.IssuedAt.Format(time.RFC3339), MaxUses: inv.MaxUses, UsedCount: inv.UsedCount,
	}
	if inv.ExpiresAt != nil {
		out.ExpiresAt = inv.ExpiresAt.Format(time.RFC3339)
	}
	return out
}

type createInviteBody struct {
	TargetGlob  string `json:"target_glob"`
	IssuedBySub string `json:"issued_by_sub"`
	MaxUses     int    `json:"max_uses"`
	ExpiresAt   string `json:"expires_at"` // RFC3339, optional
}

// handleInviteCreate is POST /v1/invites — mint an invite. Bearer scope
// invites:write. issued_by_sub defaults to the caller's verified sub when the
// body omits it (CLI passes "cli"; dashd passes the admin sub).
func (a *admin) handleInviteCreate(w http.ResponseWriter, r *http.Request) {
	if !a.authed(w, r, "invites:write") {
		return
	}
	var body createInviteBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if body.TargetGlob == "" {
		writeErr(w, http.StatusBadRequest, "missing_field", "target_glob required")
		return
	}
	if body.IssuedBySub == "" {
		body.IssuedBySub = "onbod"
	}
	var expiresAt *time.Time
	if body.ExpiresAt != "" {
		t, err := time.Parse(time.RFC3339, body.ExpiresAt)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "bad_request", "expires_at: "+err.Error())
			return
		}
		expiresAt = &t
	}
	inv, err := store.New(a.db).CreateInvite(body.TargetGlob, body.IssuedBySub, body.MaxUses, expiresAt)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "create_failed", err.Error())
		return
	}
	slog.Info("invite created", "token", inv.Token[:min(8, len(inv.Token))], "target", inv.TargetGlob)
	writeJSON(w, http.StatusOK, toInviteJSON(*inv))
}

// handleInviteList is GET /v1/invites[?issued_by=SUB] — list invites. Bearer
// scope invites:read (write covers read).
func (a *admin) handleInviteList(w http.ResponseWriter, r *http.Request) {
	if !a.authed(w, r, "invites:read", "invites:write") {
		return
	}
	invs, err := store.New(a.db).ListInvites(r.URL.Query().Get("issued_by"))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	out := make([]inviteJSON, len(invs))
	for i, inv := range invs {
		out[i] = toInviteJSON(inv)
	}
	writeJSON(w, http.StatusOK, map[string]any{"invites": out})
}

// handleInviteRevoke is DELETE /v1/invites/{token} — revoke an invite. Bearer
// scope invites:write.
func (a *admin) handleInviteRevoke(w http.ResponseWriter, r *http.Request) {
	if !a.authed(w, r, "invites:write") {
		return
	}
	token := r.PathValue("token")
	if token == "" {
		writeErr(w, http.StatusBadRequest, "missing_field", "token required")
		return
	}
	if err := store.New(a.db).RevokeInvite(token); err != nil {
		writeErr(w, http.StatusInternalServerError, "revoke_failed", err.Error())
		return
	}
	slog.Info("invite revoked", "token", token[:min(8, len(token))])
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

type insertOnboardingBody struct {
	JID string `json:"jid"`
}

// handleOnboardingInsert is POST /v1/onboarding — record a chat-initiated
// onboarding row (status awaiting_message) for an unrouted JID. routd's poll
// loop hits this on a route miss; onbod OWNS the onboarding table, so routd
// can't insert directly (it's not mounted to onbod.db). Bearer scope
// invites:write (the onboarding-admission scope family). Idempotent: store's
// INSERT OR IGNORE makes a re-post a no-op.
func (a *admin) handleOnboardingInsert(w http.ResponseWriter, r *http.Request) {
	if !a.authed(w, r, "invites:write") {
		return
	}
	var body insertOnboardingBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if body.JID == "" {
		writeErr(w, http.StatusBadRequest, "missing_field", "jid required")
		return
	}
	if err := store.New(a.db).InsertOnboarding(body.JID); err != nil {
		writeErr(w, http.StatusInternalServerError, "insert_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

type gateJSON struct {
	Gate        string `json:"gate"`
	LimitPerDay int    `json:"limit_per_day"`
	Enabled     bool   `json:"enabled"`
}

// handleGateList is GET /v1/gates — list onboarding gates. Bearer scope
// gates:read (write covers read).
func (a *admin) handleGateList(w http.ResponseWriter, r *http.Request) {
	if !a.authed(w, r, "gates:read", "gates:write") {
		return
	}
	gates, err := store.New(a.db).ListGates()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	out := make([]gateJSON, len(gates))
	for i, g := range gates {
		out[i] = gateJSON{Gate: g.Gate, LimitPerDay: g.LimitPerDay, Enabled: g.Enabled}
	}
	writeJSON(w, http.StatusOK, map[string]any{"gates": out})
}

type putGateBody struct {
	LimitPerDay int   `json:"limit_per_day"`
	Enabled     *bool `json:"enabled"` // nil → leave enablement untouched (upsert limit only)
}

// handleGatePut is PUT /v1/gates/{gate} — upsert a gate's daily limit and/or
// flip its enabled flag. Bearer scope gates:write. A body with only enabled set
// toggles; with limit_per_day set upserts the limit; both does both.
func (a *admin) handleGatePut(w http.ResponseWriter, r *http.Request) {
	if !a.authed(w, r, "gates:write") {
		return
	}
	gateName := r.PathValue("gate")
	if gateName == "" {
		writeErr(w, http.StatusBadRequest, "missing_field", "gate required")
		return
	}
	var body putGateBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body); err != nil && !errors.Is(err, sql.ErrNoRows) {
		// empty body is allowed (enable-only via the flag); a malformed body is not
		if body == (putGateBody{}) {
			writeErr(w, http.StatusBadRequest, "bad_request", err.Error())
			return
		}
	}
	st := store.New(a.db)
	if body.LimitPerDay > 0 {
		if err := st.PutGate(gateName, body.LimitPerDay); err != nil {
			writeErr(w, http.StatusInternalServerError, "put_failed", err.Error())
			return
		}
	}
	if body.Enabled != nil {
		if err := st.EnableGate(gateName, *body.Enabled); err != nil {
			writeErr(w, http.StatusInternalServerError, "enable_failed", err.Error())
			return
		}
	}
	slog.Info("gate set", "gate", gateName, "limit", body.LimitPerDay)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleGateDelete is DELETE /v1/gates/{gate} — remove a gate. Bearer scope
// gates:write.
func (a *admin) handleGateDelete(w http.ResponseWriter, r *http.Request) {
	if !a.authed(w, r, "gates:write") {
		return
	}
	gateName := r.PathValue("gate")
	if gateName == "" {
		writeErr(w, http.StatusBadRequest, "missing_field", "gate required")
		return
	}
	if err := store.New(a.db).DeleteGate(gateName); err != nil {
		writeErr(w, http.StatusInternalServerError, "delete_failed", err.Error())
		return
	}
	slog.Info("gate deleted", "gate", gateName)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
