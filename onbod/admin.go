package main

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/store"
)

// admin serves onbod's bearer-gated /v1/* surface: the invite + gate writers
// that dashd, the host CLI, and routd's /invite + /gate commands reach instead
// of touching onbod's tables directly (spec 5/5 § Daemon ownership). All
// mutations go through store.New(db) against onbod's OWNED DB (onbod.db) using
// the same audited writers.
type admin struct {
	db *sql.DB
	ks *auth.KeySet // authd JWKS; nil (AUTHD_URL unset, e.g. local-dev) → open, like routd's nil verifier
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
	if hasAnyScope(sub.Scope, anyScope...) {
		return true
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

// inviteJSON is the READ shape for an invite row. It has no token field at all:
// the bearer is shown once at creation and never again, so a list response
// cannot leak it however the handler is later edited. `ref` (store.InviteRef)
// identifies the row for DELETE /v1/invites/{ref} without being redeemable.
type inviteJSON struct {
	Ref         string `json:"ref"`
	TargetGlob  string `json:"target_glob"`
	IssuedBySub string `json:"issued_by_sub"`
	IssuedAt    string `json:"issued_at"`
	ExpiresAt   string `json:"expires_at,omitempty"`
	MaxUses     int    `json:"max_uses"`
	UsedCount   int    `json:"used_count"`
}

// inviteCreatedJSON is the CREATE-only shape: the read shape plus the raw
// bearer, returned exactly once (mirrors route_tokens' issue verbs). The
// bearer never lives on store.Invite (I1) — the caller that just minted it
// passes it in separately.
type inviteCreatedJSON struct {
	inviteJSON
	Token string `json:"token"`
}

func toInviteJSON(inv store.Invite) inviteJSON {
	out := inviteJSON{
		Ref: inv.Ref, TargetGlob: inv.TargetGlob, IssuedBySub: inv.IssuedBySub,
		IssuedAt: inv.IssuedAt.Format(time.RFC3339), MaxUses: inv.MaxUses, UsedCount: inv.UsedCount,
	}
	if inv.ExpiresAt != nil {
		out.ExpiresAt = inv.ExpiresAt.Format(time.RFC3339)
	}
	return out
}

func toInviteCreatedJSON(inv store.Invite, rawToken string) inviteCreatedJSON {
	return inviteCreatedJSON{inviteJSON: toInviteJSON(inv), Token: rawToken}
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

// handleOnboardingList is GET /v1/onboarding?status= — list onboarding rows for
// the operator dashboard queue page. Bearer scope invites:write. The token field
// is NEVER returned (live onboarding tokens are bearer credentials; spec 6/7
// "No token column").
func (a *admin) handleOnboardingList(w http.ResponseWriter, r *http.Request) {
	if !a.authed(w, r, "invites:write") {
		return
	}
	rows, err := store.New(a.db).ListOnboarding(r.URL.Query().Get("status"))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"onboarding": rows})
}

// handleOnboardingApprove is POST /v1/onboarding/{jid}/approve — operator
// fast-path admission, bypassing the gate's daily limit. Bearer scope invites:write.
func (a *admin) handleOnboardingApprove(w http.ResponseWriter, r *http.Request) {
	if !a.authed(w, r, "invites:write") {
		return
	}
	if err := store.New(a.db).ApproveOnboarding(r.PathValue("jid")); err != nil {
		writeErr(w, http.StatusInternalServerError, "approve_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleOnboardingDeny is DELETE /v1/onboarding/{jid} — deny/drop the row.
// Bearer scope invites:write.
func (a *admin) handleOnboardingDeny(w http.ResponseWriter, r *http.Request) {
	if !a.authed(w, r, "invites:write") {
		return
	}
	if err := store.New(a.db).DenyOnboarding(r.PathValue("jid")); err != nil {
		writeErr(w, http.StatusInternalServerError, "deny_failed", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleOnboardingReprompt is POST /v1/onboarding/{jid}/reprompt — resets the
// row to awaiting_message so the next poll tick resends the auth link. Bearer
// scope invites:write.
func (a *admin) handleOnboardingReprompt(w http.ResponseWriter, r *http.Request) {
	if !a.authed(w, r, "invites:write") {
		return
	}
	if err := store.New(a.db).RepromptOnboarding(r.PathValue("jid")); err != nil {
		writeErr(w, http.StatusInternalServerError, "reprompt_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
