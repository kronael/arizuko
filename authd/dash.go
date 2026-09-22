package main

// dash.go is authd's operator dashboard at GET /dash/authd/ (spec 6/28, 6/30).
// The handlers call the SAME authstore methods the /v1 resources call, in
// process.

import (
	"fmt"
	"html"
	"html/template"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/resreg/resources"
	"github.com/kronael/arizuko/theme"
)

// sessionsShown caps the login table: one row per refresh FAMILY.
const sessionsShown = 50

// handleDash renders the signing keys and who is signed in.
func (s *server) handleDash(w http.ResponseWriter, r *http.Request) {
	if !auth.RequireOperator(w, r, s.dashKeys, "authd") {
		return
	}
	now := time.Now()

	var b strings.Builder
	fmt.Fprint(&b, `<div class="crumb"><a href="/dash/services/">arizuko hub</a> › authd</div>`)
	fmt.Fprint(&b, `<h1>authd</h1>`)

	keys, err := s.a.st.SigningKeyMetadata(s.a.maxAccessTTL, now)
	if err != nil {
		slog.Error("dash: signing keys", "err", err)
		b.WriteString(`<div class="banner-err">Could not read the signing keys.</div>`)
	} else {
		b.WriteString(signingKeyTable(keys))
	}

	sessions, err := s.a.st.SessionsByFamily("", sessionsShown, now)
	if err != nil {
		slog.Error("dash: sessions", "err", err)
		b.WriteString(`<div class="banner-err">Could not read the session list.</div>`)
	} else {
		b.WriteString(sessionTable(sessions))
	}

	fmt.Fprint(w, theme.Page("authd", template.HTML(b.String())))
}

// handleDashRevoke kills one refresh family. RequireWrite: operator scope AND
// same origin, because this is a cookie-authenticated mutation that signs a
// person out.
func (s *server) handleDashRevoke(w http.ResponseWriter, r *http.Request) {
	if !auth.RequireWrite(w, r, s.dashKeys, "authd") {
		return
	}
	family := strings.TrimSpace(r.FormValue("family_id"))
	if family == "" {
		http.Error(w, "family_id required", http.StatusBadRequest)
		return
	}
	killed, err := s.a.st.RevokeFamily(family)
	if err != nil {
		slog.Error("dash revoke", "family_id", family, "err", err)
		http.Error(w, "write failed", http.StatusInternalServerError)
		return
	}
	// Killing nothing is not success: an operator mistyping an id during an
	// incident must not be told the session is dead.
	if !killed {
		auth.Forbidden(w, "authd", "No live session "+family+".")
		return
	}
	slog.Info("dash revoke", "family_id", family)
	http.Redirect(w, r, "/dash/authd/", http.StatusSeeOther)
}

// signingKeyTable renders key METADATA: only what the /v1 row carries, so the
// two faces cannot diverge.
func signingKeyTable(rows []resources.SigningKeysRow) string {
	var b strings.Builder
	b.WriteString(`<h2>Signing keys</h2>`)
	if len(rows) == 0 {
		return b.String() + `<div class="banner-err">No signing keys. authd cannot mint a token.</div>`
	}
	b.WriteString(`<table><thead><tr><th>kid</th><th>alg</th><th>status</th>` +
		`<th>created</th><th>serves until</th></tr></thead><tbody>`)
	for _, k := range rows {
		b.WriteString(fmt.Sprintf(
			`<tr><td><code>%s</code></td><td>%s</td><td class="status-%s">%s</td>`+
				`<td>%s</td><td>%s</td></tr>`,
			html.EscapeString(k.Kid), html.EscapeString(k.Alg),
			html.EscapeString(k.Status), html.EscapeString(k.Status),
			html.EscapeString(k.CreatedAt), html.EscapeString(k.ServesUntil)))
	}
	b.WriteString(`</tbody></table>`)
	return b.String()
}

// sessionTable renders one row per refresh family, not one row per token.
func sessionTable(rows []resources.SessionsRow) string {
	var b strings.Builder
	b.WriteString(`<h2>Who is signed in</h2>`)
	if len(rows) == 0 {
		return b.String() + `<p class="ok">No active sessions.</p>`
	}
	b.WriteString(`<table><thead><tr><th>sub</th><th>scope</th><th>status</th>` +
		`<th>renewed</th><th>expires</th><th>rotations</th><th></th></tr></thead><tbody>`)
	for _, s := range rows {
		b.WriteString(fmt.Sprintf(
			`<tr><td>%s</td><td>%s</td><td class="status-%s">%s</td>`+
				`<td>%s</td><td>%s</td><td>%d</td><td>%s</td></tr>`,
			html.EscapeString(s.Sub), html.EscapeString(s.Scope),
			html.EscapeString(s.Status), html.EscapeString(s.Status),
			html.EscapeString(s.RenewedAt), html.EscapeString(s.ExpiresAt),
			s.Rotations, revokeForm(s)))
	}
	b.WriteString(`</tbody></table>`)
	return b.String()
}

// revokeForm renders the sign-out button for a live family only; the writer
// reports killed=false for a revoked one.
func revokeForm(s resources.SessionsRow) string {
	if s.Status != "active" {
		return ""
	}
	return fmt.Sprintf(
		`<form method="post" action="/dash/authd/revoke">`+
			`<input type="hidden" name="family_id" value="%s">`+
			`<button type="submit">sign out</button></form>`,
		html.EscapeString(s.FamilyID))
}
