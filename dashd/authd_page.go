package main

// GET /dash/authd/ — the identity cockpit: which key signs the login passes,
// who is signed in, and the control that signs one person out (spec 5/1 DoD
// item 6).
//
// Everything here comes from authd's /v1 face. dashd is NOT FS-mounted on
// auth.db and must not become so: authd is the sole ES256 signer, so the DB
// file is the trust boundary, and a second process with a handle on it is a
// second thing that has to be right about signing_keys. HTTP is the only
// contract authd offers for these rows, and it is the contract that applies the
// scope gate and writes the revoke's audit row inside the mutation's own
// transaction.
//
// The audit rows authd emits are NOT re-rendered here. /dash/audit/ already
// federates authd's GET /v1/audit (spec 5/I), so this page links there — one
// renderer for the log, not a second one that drifts.

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/kronael/arizuko/resreg/resources"
)

// authdSessionsLimit is the rendered window. authd caps a page at 200; asking
// for fewer keeps the operator's list to the logins worth reading, and the
// table says so rather than implying it is everything.
const authdSessionsLimit = 50

// handleAuthd renders the identity cockpit. Operator-only: it lists every
// tenant's logins, and dashd presents its own empty-folder service bearer, so
// authd would not narrow the answer to a folder-scoped viewer.
func (d *dash) handleAuthd(w http.ResponseWriter, r *http.Request) {
	if !d.requireOperator(w, r) {
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	pageTopFor(w, r, "authd",
		struct{ Href, Label string }{"/dash/services/", "Services"},
		struct{ Href, Label string }{"", "authd"},
	)

	fmt.Fprint(w, `<p class="dim">When someone logs in, authd hands their browser a <strong>pass</strong> `+
		`&mdash; a small signed note saying who they are. Every other part of arizuko checks the `+
		`signature on that note instead of asking authd each time, which is why a pass has to be `+
		`signed with a key only authd holds.</p>`)

	switch strings.TrimSpace(r.URL.Query().Get("msg")) {
	case "revoked":
		fmt.Fprint(w, htmlBanner("ok", "signed out — that login can no longer renew itself"))
	}
	if e := strings.TrimSpace(r.URL.Query().Get("err")); e != "" {
		fmt.Fprint(w, htmlBanner("err", e))
	}

	d.renderSigningKeys(w, r)
	d.renderAuthdSessions(w, r)
	renderAuthdAuditLink(w)
	pageClose(w, r)
}

// renderSigningKeys writes the key-metadata table from authd's
// GET /v1/signing_keys. Metadata only — the endpoint has no key-material column
// to serve (resreg/resources.SigningKeysRow), so there is none to filter here.
func (d *dash) renderSigningKeys(w http.ResponseWriter, r *http.Request) {
	fmt.Fprint(w, `<h2>Signing keys</h2>`)
	fmt.Fprint(w, `<p class="dim">The private half of a key never leaves authd &mdash; it is not shown `+
		`on this page and there is no endpoint that would serve it. What you see is the key&rsquo;s `+
		`life story: when it started signing, when it stopped, and when the passes it signed stop `+
		`being accepted.</p>`)
	fmt.Fprint(w, `<p class="dim"><strong>active</strong> &mdash; signing every new pass right now. `+
		`<strong>retiring</strong> &mdash; signs nothing new, but passes it already signed are still `+
		`accepted until the time in the last column. `+
		`<strong>retired</strong> &mdash; signs nothing and verifies nothing; it is waiting to be cleaned up.</p>`)

	keys, ok := authdList[resources.SigningKeysRow](d, w, r, "/v1/signing_keys")
	if !ok {
		return
	}

	tableRows := make([][]string, 0, len(keys))
	for _, k := range keys {
		tableRows = append(tableRows, []string{
			`<code>` + esc(k.Kid) + `</code>`,
			dotCell(signingKeyDot(k.Status), k.Status),
			esc(k.Alg),
			absTSCell(k.CreatedAt),
			absTSCell(k.RetiredAt),
			authdServesUntilCell(k),
		})
	}
	fmt.Fprint(w, htmlTable(
		[]string{"Key", "Status", "Signed with", "Created", "Retired", "Passes accepted until"},
		tableRows,
		"No signing key yet — authd makes one the first time it starts."))

	fmt.Fprint(w, htmlBannerRaw("warn",
		`<strong>Signing everyone out at once</strong> is a different lever from the sign-out buttons below: `+
			`it means retiring the active key, which makes every pass in every browser invalid within the hour. `+
			`There is deliberately no button for it here &mdash; arizuko rotates keys out-of-band, by restarting `+
			`authd with a fresh one, so a fleet-wide logout cannot happen on a mis-click.`))
}

// renderAuthdSessions writes the login table from authd's GET /v1/sessions,
// with a per-login sign-out control.
func (d *dash) renderAuthdSessions(w http.ResponseWriter, r *http.Request) {
	fmt.Fprint(w, `<h2>Who is signed in</h2>`)
	fmt.Fprint(w, `<p class="dim">One row per login, newest first. A login lasts 30 days and renews `+
		`itself quietly in the background &mdash; <strong>renewals</strong> counts how many times it has. `+
		`arizuko stores no copy of anyone&rsquo;s pass, so nothing on this page can be used to sign in as them.</p>`)
	fmt.Fprint(w, `<p class="dim"><strong>active</strong> &mdash; signed in and renewing. `+
		`<strong>revoked</strong> &mdash; signed out early, either by that person or by an operator here. `+
		`<strong>expired</strong> &mdash; ran its 30 days and stopped.</p>`)

	sessions, ok := authdList[resources.SessionsRow](d, w, r,
		fmt.Sprintf("/v1/sessions?limit=%d", authdSessionsLimit))
	if !ok {
		return
	}

	tableRows := make([][]string, 0, len(sessions))
	for _, s := range sessions {
		tableRows = append(tableRows, []string{
			esc(s.Sub),
			dotCell(sessionDot(s.Status), s.Status),
			authdScopeCell(s.Scope),
			absTSCell(s.StartedAt),
			absTSCell(s.RenewedAt),
			absTSCell(s.ExpiresAt),
			fmt.Sprintf("%d", s.Rotations),
			authdRevokeCell(s),
		})
	}
	fmt.Fprint(w, htmlTable(
		[]string{"Person", "Status", "Can reach", "Signed in", "Last renewed", "Runs out", "Renewals", ""},
		tableRows,
		"Nobody is signed in right now."))
}

// renderAuthdAuditLink points at the federated log rather than repeating it.
// authd's rows already reach /dash/audit/ through its GET /v1/audit (spec 5/I);
// a second table here would be a second renderer of the same rows.
func renderAuthdAuditLink(w http.ResponseWriter) {
	fmt.Fprint(w, `<h2>What authd wrote down</h2>`)
	fmt.Fprint(w, `<p class="dim">Every login, and every sign-out done from this page, is recorded. `+
		`Those records live with the rest of arizuko&rsquo;s log: `+
		`<a href="/dash/audit/">open the audit log</a>. `+
		`authd&rsquo;s rows are the ones whose <em>Source</em> column says <code>authd</code>.</p>`)
}

// authdRevokeCell renders the sign-out control for one login.
//
// Behind a confirm, like every other dashd danger-zone action, and the confirm
// spells out the delay rather than promising an instant cut: revoking stops the
// RENEWAL, and the pass already in that browser keeps working until its own
// short expiry (spec 5/1 § Revocation = short-TTL only). An operator told
// "signed out" who then watched the person keep clicking for ten minutes would
// reasonably conclude the button was broken.
//
// A login that is already revoked or expired gets no button: authd answers 404
// for a family that is not live, so the control would only ever produce an
// error banner.
func authdRevokeCell(s resources.SessionsRow) string {
	if s.Status != "active" {
		return `<span class="dim">&mdash;</span>`
	}
	return fmt.Sprintf(
		`<form method="post" action="/dash/authd/revoke" class="form-inline"`+
			` onsubmit="return confirm('Sign %s out? Their browser drops back to the login screen. `+
			`A page they already have open keeps working for up to 15 more minutes, then stops too.')">`+
			`<input type="hidden" name="family_id" value="%s">`+
			`<button class="btn-danger btn-sm" type="submit">sign out</button></form>`,
		esc(s.Sub), esc(s.FamilyID))
}

// handleAuthdRevoke handles POST /dash/authd/revoke — end one login through
// authd's DELETE /v1/sessions/{family_id}.
//
// The audit row is authd's, not dashd's: resreg.invoke opens the transaction,
// runs the revoke in it, and emits the event into the same one, rolling the
// mutation back if the audit write fails (spec 5/1 § Where the revoke's audit
// row lands). Emitting a second row from here would put dashd's claim that a
// revoke happened next to authd's record that it did, with nothing keeping the
// two honest — which is the drift a single writer exists to prevent.
func (d *dash) handleAuthdRevoke(w http.ResponseWriter, r *http.Request) {
	if !d.requireOperator(w, r) {
		return
	}
	if !requireSameOrigin(w, r) {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	family := strings.TrimSpace(r.FormValue("family_id"))
	if family == "" {
		http.Error(w, "family_id required", http.StatusBadRequest)
		return
	}
	if d.authdURL == "" {
		authdRedirect(w, r, "", "AUTHD_URL not configured")
		return
	}
	body, status, err := d.bearerCall(r.Context(), d.authdURL, http.MethodDelete,
		"/v1/sessions/"+url.PathEscape(family), nil)
	if err != nil {
		slog.Warn("authd revoke: call", "family_id", family, "err", err)
		authdRedirect(w, r, "", "could not reach authd: "+err.Error())
		return
	}
	if status < 200 || status >= 300 {
		slog.Warn("authd revoke: status", "family_id", family, "status", status)
		authdRedirect(w, r, "", upstreamErr("authd", status, body))
		return
	}
	slog.Info("authd session revoked", "family_id", family)
	authdRedirect(w, r, "revoked", "")
}

func authdRedirect(w http.ResponseWriter, r *http.Request, msg, errMsg string) {
	dest := "/dash/authd/?msg=" + msg
	if errMsg != "" {
		dest = "/dash/authd/?err=" + url.QueryEscape(errMsg)
	}
	http.Redirect(w, r, dest, http.StatusSeeOther)
}

// authdList GETs one of authd's list faces and reports whether the caller may
// render the result. A transport failure, a non-2xx or an undecodable body each
// raise a banner and return false — this page must never render a failed read
// as an empty table, because "nobody is signed in" and "authd did not answer"
// are the two answers an operator must never confuse.
//
// A free function rather than a method because Go methods take no type
// parameters, and the alternative — an `any` out-param plus a cast at each call
// site — moves a compile-time check to runtime for nothing.
func authdList[T any](d *dash, w http.ResponseWriter, r *http.Request, path string) ([]T, bool) {
	if d.authdURL == "" {
		fmt.Fprint(w, htmlBanner("err", "AUTHD_URL not configured"))
		return nil, false
	}
	body, status, err := d.bearerCall(r.Context(), d.authdURL, http.MethodGet, path, nil)
	if err != nil {
		slog.Warn("authd page: call", "path", path, "err", err)
		fmt.Fprint(w, htmlBanner("err", "could not reach authd: "+err.Error()))
		return nil, false
	}
	if status != http.StatusOK {
		slog.Warn("authd page: status", "path", path, "status", status)
		fmt.Fprint(w, htmlBanner("err", upstreamErr("authd", status, body)))
		return nil, false
	}
	var out []T
	if err := json.Unmarshal(body, &out); err != nil {
		slog.Warn("authd page: decode", "path", path, "err", err)
		fmt.Fprint(w, htmlBanner("err", "could not read authd's answer: "+err.Error()))
		return nil, false
	}
	return out, true
}

// signingKeyDot maps a key's serving state to its dot class. retiring is warn
// rather than err: a key inside its overlap window is a rotation working as
// designed, not a fault.
func signingKeyDot(status string) string {
	switch status {
	case "active":
		return "ok"
	case "retiring":
		return "warn"
	default:
		return "unknown"
	}
}

// sessionDot maps a login's state to its dot class. revoked is err because it
// is the one state somebody chose — an incident review looks for it.
func sessionDot(status string) string {
	switch status {
	case "active":
		return "ok"
	case "revoked":
		return "err"
	default:
		return "unknown"
	}
}

// dotCell renders a status dot plus its label, the services-hub vocabulary
// (dot-ok / dot-warn / dot-err / dot-unknown).
func dotCell(dot, label string) string {
	return fmt.Sprintf(`<span class="dot dot-%s"></span>%s`, esc(dot), esc(label))
}

// absTSCell renders a timestamp as its relative age with the exact instant in
// the tooltip, the shape every other dashd table uses. Empty renders as an
// em-dash so the column reads as answered rather than as data dashd lost.
func absTSCell(ts string) string {
	if ts == "" {
		return `<span class="dim">&mdash;</span>`
	}
	return fmt.Sprintf(`<abbr title="%s">%s</abbr>`, esc(ts), esc(relativeTS(ts)))
}

// authdServesUntilCell renders when a retired key stops being accepted. An
// active key has no such instant — it is still signing — so it says so instead
// of showing a blank the operator would read as "unknown".
func authdServesUntilCell(k resources.SigningKeysRow) string {
	if k.ServesUntil == "" {
		if k.Active {
			return `<span class="dim">still signing</span>`
		}
		return `<span class="dim">&mdash;</span>`
	}
	return fmt.Sprintf(`<abbr title="%s">%s</abbr>`, esc(k.ServesUntil), esc(remainingTS(k.ServesUntil)))
}

// authdScopeCell renders what a login can reach. An empty scope is the
// authenticated-but-unauthorized session authd mints when the grants backend
// has nothing for the account (spec 5/1 § Login-time scope snapshot) — it lands
// the user on /onboard, so naming it is more useful than a blank cell.
func authdScopeCell(scope string) string {
	if strings.TrimSpace(scope) == "" {
		return `<span class="dim">nothing yet</span>`
	}
	return `<code>` + esc(scope) + `</code>`
}
