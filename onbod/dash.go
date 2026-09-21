package main

import (
	"fmt"
	"html"
	"html/template"
	"log/slog"
	"net/http"
	"strings"

	"github.com/kronael/arizuko/auth"
)

// handleDash renders the operator admission-queue page: every onboarding row
// with approve/deny/reprompt action forms. Reads onbod's OWNED onboarding
// table directly (a.st); the action forms POST to the dash mutation
// shims (handleDashApprove/Deny/Reprompt), which call the SAME store writers
// the /v1 handlers use — one writer per resource, no parallel path. There is
// nothing credential-shaped left to render: onbod 0006 dropped the last
// token column, so the table holds admission facts and nothing else.
func (a *admin) handleDash(w http.ResponseWriter, r *http.Request) {
	if !auth.RequireOperator(w, r, a.ks, "onbod") {
		return
	}
	rows, err := a.st.ListOnboarding("")
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
//
// Reprompt is the operator's BYPASS of the greeting cooldown, not the only way
// to re-greet: a chat that messages again past store.PairingTTL gets a fresh
// link on its own (spec 5/31). It resets the row completely, admission verdict
// included, so this row goes back to the start of the queue.
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
// the queue. The store method is the single writer per resource (root
// CLAUDE.md § "One renderer, many sinks") — these and the /v1 handlers share it.
func (a *admin) handleDashApprove(w http.ResponseWriter, r *http.Request) {
	if !auth.RequireOperator(w, r, a.ks, "onbod") {
		return
	}
	if err := a.st.ApproveOnboarding(r.PathValue("jid")); err != nil {
		slog.Error("dash: approve", "jid", r.PathValue("jid"), "err", err)
		http.Error(w, "approve failed", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/dash/onbod/", http.StatusSeeOther)
}

func (a *admin) handleDashDeny(w http.ResponseWriter, r *http.Request) {
	if !auth.RequireOperator(w, r, a.ks, "onbod") {
		return
	}
	if err := a.st.DenyOnboarding(r.PathValue("jid")); err != nil {
		slog.Error("dash: deny", "jid", r.PathValue("jid"), "err", err)
		http.Error(w, "deny failed", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/dash/onbod/", http.StatusSeeOther)
}

func (a *admin) handleDashReprompt(w http.ResponseWriter, r *http.Request) {
	if !auth.RequireOperator(w, r, a.ks, "onbod") {
		return
	}
	if err := a.st.RepromptOnboarding(r.PathValue("jid")); err != nil {
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
