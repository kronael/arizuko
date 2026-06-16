package main

import (
	"encoding/json"
	"fmt"
	"html"
	"html/template"
	"log/slog"
	"net/http"
	"strings"

	"github.com/kronael/arizuko/store"
)

// requireOperator gates onbod's /dash/ surface to an operator (`**` in the
// proxyd-stamped X-User-Groups). This is distinct from the bearer-scoped /v1
// gate (admin.authed): the dashboard trusts the proxyd-verified end-user
// identity, not a service bearer. Mirrors dashd's callerScope `**` check.
// nil ks (local dev / tests, AUTHD_URL unset) → open, like the /v1 gate.
func (a *admin) requireOperator(w http.ResponseWriter, r *http.Request) bool {
	if a.ks == nil {
		return true
	}
	for _, g := range callerGroups(r) {
		if g == "**" {
			return true
		}
	}
	http.Error(w, "forbidden", http.StatusForbidden)
	return false
}

// callerGroups parses the proxyd-signed X-User-Groups header (JSON array of the
// folders the caller holds an allow-grant on, store.UserScopes output at sign
// time). Operator = `**` in the set.
func callerGroups(r *http.Request) []string {
	hdr := r.Header.Get("X-User-Groups")
	if hdr == "" {
		return nil
	}
	var groups []string
	_ = json.Unmarshal([]byte(hdr), &groups)
	return groups
}

// handleDash renders the operator admission-queue page: every onboarding row
// with approve/deny/reprompt action forms. Reads onbod's OWNED onboarding
// table directly (store.New(a.db)); the action forms POST to the dash mutation
// shims (handleDashApprove/Deny/Reprompt), which call the SAME store writers
// the /v1 handlers use — one writer per resource, no parallel path. The token
// column is never read or rendered (a live onboarding token is a bearer
// credential; spec 6/7).
func (a *admin) handleDash(w http.ResponseWriter, r *http.Request) {
	if !a.requireOperator(w, r) {
		return
	}
	rows, err := store.New(a.db).ListOnboarding("")
	if err != nil {
		slog.Error("dash: list onboarding", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	var b strings.Builder
	b.WriteString(`<table><tr><th>JID</th><th>Status</th><th>Gate</th><th>Created</th><th>Actions</th></tr>`)
	for _, row := range rows {
		fmt.Fprintf(&b,
			`<tr><td>%s</td><td>%s</td><td>%s</td><td class="dim">%s</td><td>%s</td></tr>`,
			esc(row.JID), esc(row.Status), esc(emptyDash(row.Gate)),
			esc(row.Created), actionForms(row.JID))
	}
	if len(rows) == 0 {
		b.WriteString(`<tr><td colspan="5" class="empty">No onboarding rows.</td></tr>`)
	}
	b.WriteString(`</table>`)

	renderPage(w, "Onboarding Queue", template.HTML(`<div class="tile">`+b.String()+`</div>`))
}

// actionForms renders the approve/deny/reprompt buttons for one row. Each is a
// same-origin POST to a dash mutation shim. Deny is .btn-danger (drops the row;
// the JID restarts onboarding on its next message). All HTML form POSTs — no JS.
func actionForms(jid string) string {
	e := esc(jid)
	return fmt.Sprintf(
		`<form class="form-inline" method="POST" action="/dash/onbod/approve/%s"><button type="submit">approve</button></form> `+
			`<form class="form-inline" method="POST" action="/dash/onbod/deny/%s"><button type="submit" class="btn-danger">deny</button></form> `+
			`<form class="form-inline" method="POST" action="/dash/onbod/reprompt/%s"><button type="submit" class="btn-secondary">reprompt</button></form>`,
		e, e, e)
}

// handleDashApprove/Deny/Reprompt are the dash mutation shims: operator-gated,
// they call the SAME store writers the /v1 handlers use, then redirect back to
// the queue. The store method is the single writer per resource (spec 6/7
// "one renderer per resource") — these and the /v1 handlers share it.
func (a *admin) handleDashApprove(w http.ResponseWriter, r *http.Request) {
	if !a.requireOperator(w, r) {
		return
	}
	if err := store.New(a.db).ApproveOnboarding(r.PathValue("jid")); err != nil {
		slog.Error("dash: approve", "jid", r.PathValue("jid"), "err", err)
		http.Error(w, "approve failed", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/dash/onbod/", http.StatusSeeOther)
}

func (a *admin) handleDashDeny(w http.ResponseWriter, r *http.Request) {
	if !a.requireOperator(w, r) {
		return
	}
	if err := store.New(a.db).DenyOnboarding(r.PathValue("jid")); err != nil {
		slog.Error("dash: deny", "jid", r.PathValue("jid"), "err", err)
		http.Error(w, "deny failed", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/dash/onbod/", http.StatusSeeOther)
}

func (a *admin) handleDashReprompt(w http.ResponseWriter, r *http.Request) {
	if !a.requireOperator(w, r) {
		return
	}
	if err := store.New(a.db).RepromptOnboarding(r.PathValue("jid")); err != nil {
		slog.Error("dash: reprompt", "jid", r.PathValue("jid"), "err", err)
		http.Error(w, "reprompt failed", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/dash/onbod/", http.StatusSeeOther)
}

func esc(s string) string { return html.EscapeString(s) }

func emptyDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}
