package main

import (
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/store"
)

// commonActions is the dropdown list for the "Add grant" form.
var commonActions = []string{
	"admin",
	"send", "send_file", "reply",
	"post", "like", "dislike", "delete", "edit", "forward", "quote", "repost",
	"register_group", "escalate_group", "delegate_group",
	"get_routes", "set_routes", "add_route", "delete_route",
	"list_tasks", "schedule_task", "pause_task", "resume_task", "cancel_task",
	"reset_session", "fork_topic", "inject_message",
	"list_acl", "get_grants", "set_grants",
	"invite_create",
	"set_group_open",
}

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
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	pageTopFor(w, r, "Grants — "+folder,
		struct{ Href, Label string }{"/dash/groups/", "Groups"},
		struct{ Href, Label string }{"", folder},
		struct{ Href, Label string }{"", "Grants"},
	)

	if d.adminDB() == nil {
		fmt.Fprint(w, htmlBanner("err", "store unavailable"))
		pageClose(w, r)
		return
	}

	s := store.New(d.adminDB())
	rows := s.ListACLByScope(folder)

	fmt.Fprintf(w, `<p class="dim">ACL rows scoped to <code>%s</code>.</p>`, esc(folder))

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
		tableRows[i] = []string{
			`<code>` + esc(row.Principal) + `</code>`,
			esc(row.Action),
			esc(row.Effect),
			`<code>` + esc(row.Params) + `</code>`,
			`<code>` + esc(row.Predicate) + `</code>`,
			esc(row.GrantedBy),
			revokeBtn,
		}
	}
	fmt.Fprint(w, htmlTable(
		[]string{"principal", "action", "effect", "params", "predicate", "granted_by", ""},
		tableRows,
	))

	// Add grant form.
	var actionSelect strings.Builder
	actionSelect.WriteString(`<select name="action">`)
	for _, a := range commonActions {
		fmt.Fprintf(&actionSelect, `<option value="%s">%s</option>`, esc(a), esc(a))
	}
	actionSelect.WriteString(`</select>`)

	fmt.Fprintf(w, `<h2>Add grant</h2><form method="post" action="/dash/groups/%s/grants">`, folderPath(folder))
	fmt.Fprint(w, htmlFormRow("principal", `<input type="text" name="principal" required size="40" placeholder="user@example or group:name">`))
	fmt.Fprint(w, htmlFormRow("action", actionSelect.String()))
	fmt.Fprint(w, htmlFormRow("effect", `<select name="effect"><option value="allow">allow</option><option value="deny">deny</option></select>`))
	fmt.Fprint(w, htmlFormRow("params", `<input type="text" name="params" size="40" placeholder="jid=telegram:* (leave blank for none)">`))
	fmt.Fprintf(w, htmlFormRow("scope", `<input type="text" name="scope" size="40" value="%s">`), esc(folder))
	fmt.Fprint(w, `<p><button type="submit">add grant</button></p></form>`)

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
	if effect != "allow" && effect != "deny" {
		effect = "allow"
	}
	if scope == "" {
		scope = folder
	}
	// Prevent scope widening: the submitted scope must be the folder itself
	// or a descendant. Without this, an admin on "alice" could insert a "**" row.
	if scope != folder && !strings.HasPrefix(scope, folder+"/") {
		http.Error(w, "scope must be within folder", http.StatusBadRequest)
		return
	}
	s := store.New(d.adminDB())
	row := core.ACLRow{
		Principal: principal,
		Action:    action,
		Scope:     scope,
		Effect:    effect,
		Params:    params,
		GrantedBy: sub,
	}
	// PutACLRow (not AddACLRow): routd.db has no audit_log table, so the
	// audit-free writer is used — same discipline as the CLI grant path.
	if err := s.PutACLRow(row); err != nil {
		slog.Warn("grants add: insert", "folder", folder, "err", err)
		http.Error(w, "insert failed", http.StatusInternalServerError)
		return
	}
	slog.Info("grant added", "folder", folder, "principal", principal, "action", action, "sub", sub)
	http.Redirect(w, r, "/dash/groups/"+folderPath(folder)+"/grants", http.StatusSeeOther)
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
	s := store.New(d.adminDB())
	row := core.ACLRow{
		Principal: principal,
		Action:    action,
		Scope:     folder,
		Effect:    effect,
		Params:    params,
		Predicate: predicate,
	}
	// RemoveACLRowBare (not RemoveACLRow): audit-free for routd.db.
	if err := s.RemoveACLRowBare(row); err != nil {
		slog.Warn("grants revoke: delete", "folder", folder, "err", err)
		http.Error(w, "delete failed", http.StatusInternalServerError)
		return
	}
	slog.Info("grant revoked", "folder", folder, "principal", principal, "action", action, "sub", sub)
	http.Redirect(w, r, "/dash/groups/"+folderPath(folder)+"/grants", http.StatusSeeOther)
}
