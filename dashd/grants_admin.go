package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/groupfolder"
	"github.com/kronael/arizuko/ipc"
	"github.com/kronael/arizuko/routd"
	"github.com/kronael/arizuko/store"
)

// GET /dash/groups/{folder}/grants — read-only view + add/revoke forms.
func (d *dash) handleGroupGrants(w http.ResponseWriter, r *http.Request) {
	folder := groupFromPath(r, "/grants")
	if folder == "" {
		http.Error(w, "bad folder", http.StatusBadRequest)
		return
	}
	if _, ok := d.requireAdmin(w, r, folder); !ok {
		return
	}
	pageTopFor(w, r, "Grants — "+folder,
		crumb{"/dash/groups/", "Groups"},
		crumb{"", folder},
		crumb{"", "Grants"},
	)
	// The alias warning from a just-written grant rides back as ?err=.
	writeFlash(w, r, nil)

	_, _ = fmt.Fprint(w, `<p><a href="/dash/groups/`+folderPath(folder)+`/settings">← Back to settings</a></p>`)

	if d.routdStore() == nil {
		_, _ = fmt.Fprint(w, htmlBanner("err", "store unavailable"))
		pageClose(w, r)
		return
	}

	// READ through routd's /v1 (spec 6/28 §"Read-path: /v1 only, no direct DB").
	// routd owns the acl table and its list filters `scope = folder`, which is
	// exactly what ListACLByScope queried — the same rows, one reader.
	//
	// The three datalist reads below (user_profiles, acl_membership, the group
	// subtree) still go to the DB: they are form autocomplete and routd exposes
	// no /v1 for them. Different data, different reader — not two paths to one
	// datum. See 6/29 for what closing them needs.
	//
	// The WRITES also stay: they carry the operator onto the audit row through
	// AsUser and dashd's service bearer does not, so moving them waits on
	// 6/29's identity decision.
	rows, err := d.listACL(r.Context(), folder)
	if err != nil {
		// An empty grants table would read as "this folder has no permissions",
		// which is the opposite of "we could not read them" (BUGS A4).
		slog.Error("dashd: grants read failed", "folder", folder, "err", err)
		_, _ = fmt.Fprint(w, htmlBanner("err", "could not read grants: "+esc(err.Error())))
		pageClose(w, r)
		return
	}

	_, _ = fmt.Fprintf(w, `<p class="dim">ACL rows scoped to <code>%s</code>.</p>`, esc(folder))
	_, _ = fmt.Fprint(w, `<p>Each row is a permission: who may do what, and when.</p>`)

	tableRows := make([][]string, len(rows))
	for i, row := range rows {
		revokeBtn := fmt.Sprintf(
			`<form method="post" action="/dash/groups/%s/grants/revoke">`+
				`<input type="hidden" name="principal" value="%s">`+
				`<input type="hidden" name="action" value="%s">`+
				`<input type="hidden" name="effect" value="%s">`+
				`<input type="hidden" name="params" value="%s">`+
				`<input type="hidden" name="predicate" value="%s">`+
				`<button type="submit" onclick="return confirm('Revoke this grant?')" `+
				`class="btn-danger">revoke</button>`+
				`</form>`,
			folderPath(folder),
			esc(row.Principal), esc(row.Action), esc(row.Effect),
			esc(row.Params), esc(row.Predicate),
		)
		grantedAt := abbrTS(row.GrantedAt)
		tableRows[i] = []string{
			`<code>` + esc(row.Principal) + `</code>`,
			esc(row.Action),
			esc(row.Effect),
			`<code>` + esc(row.Params) + `</code>`,
			`<code>` + esc(row.Predicate) + `</code>`,
			esc(row.GrantedBy),
			grantedAt,
			revokeBtn,
		}
	}
	_, _ = fmt.Fprint(w, htmlTable(
		[]string{"who", "can do what", "allow or deny", "only these", "only when", "granted by", "granted", ""},
		tableRows,
		"No grants for this group.",
	))

	var principalOpts strings.Builder
	principalOpts.WriteString(`<datalist id="principals">`)
	// routd owns user_profiles and acl_membership; the form asks for the subs a
	// grant may name rather than reading either table (6/28, BUGS L142). The
	// projection is sub + name — a label and an id, nothing else.
	if ps, err := d.principalsViaRoutd(r.Context()); err == nil {
		for _, pr := range ps {
			label := pr.Sub
			if pr.Name != "" && pr.Name != pr.Sub {
				label += " (" + pr.Name + ")"
			}
			fmt.Fprintf(&principalOpts, `<option value="%s" label="%s">`, esc(pr.Sub), esc(label))
		}
	} else {
		slog.Warn("grants: principals via routd", "err", err)
	}
	principalOpts.WriteString(`</datalist>`)

	// Scope options are the current folder and its descendants only — the POST
	// handler rejects anything else (no widening to ** or siblings), so the UI
	// must not offer options the server would reject.
	var scopeSelect strings.Builder
	scopeSelect.WriteString(`<select name="scope">`)
	fmt.Fprintf(&scopeSelect, `<option value="%s" selected>%s (this folder)</option>`, esc(folder), esc(folder))
	// routd owns `groups`, so the subtree is filtered from ITS answer rather
	// than from its file (6/28). Containment is groupfolder.Contains — the same
	// rule the POST handler rejects a widened scope with, so the UI cannot
	// offer an option the server would refuse.
	if all, err := d.foldersViaRoutd(r.Context()); err == nil {
		for _, gf := range all {
			// The folder itself is already the selected option above.
			if gf != folder && groupfolder.Contains(folder, gf) {
				fmt.Fprintf(&scopeSelect, `<option value="%s">%s</option>`, esc(gf), esc(gf))
			}
		}
	} else {
		slog.Warn("grants: scope options via routd", "err", err)
	}
	scopeSelect.WriteString(`</select>`)

	var actionInput strings.Builder
	actionInput.WriteString(`<input type="text" name="action" required list="actions" value="admin" ` +
		`placeholder="mcp:send or shell:env:KEY">` +
		`<datalist id="actions">`)
	for _, action := range store.ACLActionChoices() {
		fmt.Fprintf(&actionInput, `<option value="%s">`, esc(action))
	}
	for _, tool := range ipc.ListTools(folder, func(string) bool { return true }) {
		fmt.Fprintf(&actionInput, `<option value="mcp:%s">`, esc(tool.Name))
	}
	actionInput.WriteString(`</datalist>`)

	_, _ = fmt.Fprintf(w, `<h2>Add grant</h2><form method="post" action="/dash/groups/%s/grants">`, folderPath(folder))
	_, _ = fmt.Fprint(w, principalOpts.String())
	_, _ = fmt.Fprint(w, htmlFormRow("principal",
		`<input type="text" name="principal" required list="principals" autocomplete="off" `+
			`size="40" placeholder="google:123 or local:admin">`))
	_, _ = fmt.Fprint(w, htmlFormRow("action", actionInput.String()))
	_, _ = fmt.Fprint(w, htmlFormRow("effect", `<select name="effect"><option value="allow">allow</option><option value="deny">deny</option></select>`))
	_, _ = fmt.Fprint(w, htmlFormRow("params", `<input type="text" name="params" size="40" placeholder="jid=telegram:* (blank = any)">`))
	_, _ = fmt.Fprint(w, htmlFormRow("scope", scopeSelect.String()))
	_, _ = fmt.Fprint(w, `<p><button type="submit">add grant</button></p></form>`)

	pageClose(w, r)
}

// POST /dash/groups/{folder}/grants — insert one ACL row.
func (d *dash) handleGroupGrantAdd(w http.ResponseWriter, r *http.Request) {
	folder := groupFromPath(r, "/grants")
	if folder == "" {
		http.Error(w, "bad folder", http.StatusBadRequest)
		return
	}
	sub, ok := d.requireAdmin(w, r, folder)
	if !ok {
		return
	}
	if d.routdStore() == nil {
		_, _ = fmt.Fprint(w, htmlBanner("err", "store unavailable"))
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	principal := strings.TrimSpace(r.FormValue("principal"))
	action := strings.TrimSpace(r.FormValue("action"))
	effect := strings.TrimSpace(r.FormValue("effect"))
	params := strings.TrimSpace(r.FormValue("params"))
	scope := strings.TrimSpace(r.FormValue("scope"))
	if principal == "" || action == "" {
		http.Error(w, "principal and action required", http.StatusBadRequest)
		return
	}
	if err := store.ValidateACLAction(action); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	// Empty = omitted field → default allow. Any other value the action does not
	// take is a malformed submission, not a default.
	if effect == "" {
		effect = "allow"
	} else if !store.EffectAllowed(effect) {
		http.Error(w, "effect must be allow or deny", http.StatusBadRequest)
		return
	}
	if scope == "" {
		scope = folder
	}
	// Prevent scope widening: the submitted scope must be the folder itself
	// or a descendant. Without this, an admin on "alice" could insert a "**" row.
	if !groupfolder.Contains(folder, scope) {
		http.Error(w, "scope must be within folder", http.StatusBadRequest)
		return
	}
	s := d.routdStore()
	row := core.ACLRow{
		Principal: principal,
		Action:    action,
		Scope:     scope,
		Effect:    effect,
		Params:    params,
		GrantedBy: sub,
	}
	// AddACLRow, the audited writer: routd.db HAS had audit_log since routd
	// migration 0016, so the grant lands with its audit row in one transaction.
	// Same INSERT OR IGNORE as PutACLRow; the actor comes from row.GrantedBy.
	if err := s.AddACLRow(row); err != nil {
		slog.Warn("grants add: insert", "folder", folder, "err", err)
		http.Error(w, "insert failed", http.StatusInternalServerError)
		return
	}
	slog.Info("grant added", "folder", folder, "principal", principal, "action", action, "sub", sub)
	// This form writes the store directly, bypassing routd's acl handler and
	// the warning it has rendered since N2 (BUGS L152). Fail-open like that
	// one — the grant is written either way — and carried back through the
	// flash the page already renders.
	dest := "/dash/groups/" + folderPath(folder) + "/grants"
	if warn := routd.NonCanonicalWarning(d.identityResolver(), principal); warn != "" {
		slog.Warn("grant added against a non-canonical principal", "folder", folder,
			"principal", principal, "warning", warn)
		dest += "?err=" + url.QueryEscape(warn)
	}
	http.Redirect(w, r, dest, http.StatusSeeOther)
}

// identityResolver builds the authd client the alias check needs. nil without a
// service token to authenticate with — as NewIdentityResolver is itself nil
// without a URL — which NonCanonicalWarning reads as "cannot check".
func (d *dash) identityResolver() routd.IdentityResolver {
	if d.svc == nil {
		return nil
	}
	return routd.NewIdentityResolver(d.authdURL, d.svc)
}

// POST /dash/groups/{folder}/grants/revoke — delete one ACL row.
func (d *dash) handleGroupGrantRevoke(w http.ResponseWriter, r *http.Request) {
	folder := groupFromPath(r, "/grants/revoke")
	if folder == "" {
		http.Error(w, "bad folder", http.StatusBadRequest)
		return
	}
	sub, ok := d.requireAdmin(w, r, folder)
	if !ok {
		return
	}
	if d.routdStore() == nil {
		_, _ = fmt.Fprint(w, htmlBanner("err", "store unavailable"))
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	principal := strings.TrimSpace(r.FormValue("principal"))
	action := strings.TrimSpace(r.FormValue("action"))
	effect := strings.TrimSpace(r.FormValue("effect"))
	params := strings.TrimSpace(r.FormValue("params"))
	predicate := strings.TrimSpace(r.FormValue("predicate"))
	if principal == "" || action == "" {
		http.Error(w, "principal and action required", http.StatusBadRequest)
		return
	}
	if effect == "" {
		effect = "allow"
	}
	s := d.routdStore()
	row := core.ACLRow{
		Principal: principal,
		Action:    action,
		Scope:     folder,
		Effect:    effect,
		Params:    params,
		Predicate: predicate,
	}
	// RemoveACLRow, the audited writer (routd.db has audit_log since routd
	// migration 0016). AsUser attributes the revoke to the operator — unlike a
	// grant, the row carries no field naming who removed it.
	if err := s.AsUser(sub).RemoveACLRow(row); err != nil {
		slog.Warn("grants revoke: delete", "folder", folder, "err", err)
		http.Error(w, "delete failed", http.StatusInternalServerError)
		return
	}
	slog.Info("grant revoked", "folder", folder, "principal", principal, "action", action, "sub", sub)
	http.Redirect(w, r, "/dash/groups/"+folderPath(folder)+"/grants", http.StatusSeeOther)
}

// listACL reads one folder's grants from routd's /v1 face.
//
// routd's list answers {"folder":…,"acl":[…]} and filters on an EXACT scope
// match, which is what store.ListACLByScope did (`WHERE scope = ?`). An
// envelope, not a bare array — decoded as such rather than assumed, because
// the sibling route_tokens face returns a bare array and guessing wrong here
// yields an empty grants table, the A4 confusion this page exists to avoid.
func (d *dash) listACL(ctx context.Context, folder string) ([]core.ACLRow, error) {
	body, status, err := d.bearerCall(ctx, d.routdURL, http.MethodGet,
		"/v1/acl?folder="+url.QueryEscape(folder), nil)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("routd /v1/acl: status %d", status)
	}
	var env struct {
		ACL []core.ACLRow `json:"acl"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, err
	}
	return env.ACL, nil
}
