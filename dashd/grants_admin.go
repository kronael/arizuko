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
	folder := r.PathValue("folder")
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

	if d.dbRW == nil {
		fmt.Fprint(w, `<div class="banner-err">store unavailable</div>`)
		pageClose(w, r)
		return
	}

	s := store.New(d.dbRW)
	rows := s.ListACLByScope(folder)

	fmt.Fprintf(w, `<p class="dim">ACL rows scoped to <code>%s</code>.</p>`, esc(folder))

	if len(rows) == 0 {
		fmt.Fprint(w, `<p class="dim">No grant rows for this folder.</p>`)
	} else {
		fmt.Fprint(w, `<table style="width:100%;border-collapse:collapse">
<thead><tr>
<th style="text-align:left;padding:4px 8px">principal</th>
<th style="text-align:left;padding:4px 8px">action</th>
<th style="text-align:left;padding:4px 8px">effect</th>
<th style="text-align:left;padding:4px 8px">params</th>
<th style="text-align:left;padding:4px 8px">predicate</th>
<th style="text-align:left;padding:4px 8px">granted_by</th>
<th></th>
</tr></thead><tbody>`)
		for _, row := range rows {
			fmt.Fprintf(w,
				`<tr><td style="padding:4px 8px;font-family:monospace">%s</td>`+
					`<td style="padding:4px 8px">%s</td>`+
					`<td style="padding:4px 8px">%s</td>`+
					`<td style="padding:4px 8px;font-family:monospace">%s</td>`+
					`<td style="padding:4px 8px;font-family:monospace">%s</td>`+
					`<td style="padding:4px 8px">%s</td>`+
					`<td><form method="post" action="/dash/groups/%s/grants/revoke">`+
					`<input type="hidden" name="principal" value="%s">`+
					`<input type="hidden" name="action" value="%s">`+
					`<input type="hidden" name="effect" value="%s">`+
					`<input type="hidden" name="params" value="%s">`+
					`<input type="hidden" name="predicate" value="%s">`+
					`<button type="submit" onclick="return confirm('Revoke this grant?')" `+
					`style="color:#b00;background:none;border:none;cursor:pointer">revoke</button>`+
					`</form></td></tr>`,
				esc(row.Principal), esc(row.Action), esc(row.Effect),
				esc(row.Params), esc(row.Predicate), esc(row.GrantedBy),
				esc(folder),
				esc(row.Principal), esc(row.Action), esc(row.Effect),
				esc(row.Params), esc(row.Predicate),
			)
		}
		fmt.Fprint(w, `</tbody></table>`)
	}

	// Add grant form.
	fmt.Fprintf(w, `<h2>Add grant</h2>
<form method="post" action="/dash/groups/%s/grants">
<p><label>principal <input type="text" name="principal" required size="40" placeholder="user@example or group:name"></label></p>
<p><label>action <select name="action">`, esc(folder))
	for _, a := range commonActions {
		fmt.Fprintf(w, `<option value="%s">%s</option>`, esc(a), esc(a))
	}
	fmt.Fprintf(w, `</select></label></p>
<p><label>effect <select name="effect"><option value="allow">allow</option><option value="deny">deny</option></select></label></p>
<p><label>params <input type="text" name="params" size="40" placeholder='jid=telegram:* (leave blank for none)'></label></p>
<p><label>scope <input type="text" name="scope" size="40" value="%s"></label></p>
<p><button type="submit">add grant</button></p>
</form>`, esc(folder))

	pageClose(w, r)
}

// POST /dash/groups/{folder}/grants — insert one ACL row.
func (d *dash) handleGroupGrantAdd(w http.ResponseWriter, r *http.Request) {
	folder := r.PathValue("folder")
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
	s := store.New(d.dbRW)
	row := core.ACLRow{
		Principal: principal,
		Action:    action,
		Scope:     scope,
		Effect:    effect,
		Params:    params,
		GrantedBy: sub,
	}
	if err := s.AddACLRow(row); err != nil {
		slog.Warn("grants add: insert", "folder", folder, "err", err)
		http.Error(w, "insert failed", http.StatusInternalServerError)
		return
	}
	slog.Info("grant added", "folder", folder, "principal", principal, "action", action, "sub", sub)
	http.Redirect(w, r, "/dash/groups/"+folderPath(folder)+"/grants", http.StatusSeeOther)
}

// POST /dash/groups/{folder}/grants/revoke — delete one ACL row.
func (d *dash) handleGroupGrantRevoke(w http.ResponseWriter, r *http.Request) {
	folder := r.PathValue("folder")
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
	s := store.New(d.dbRW)
	row := core.ACLRow{
		Principal: principal,
		Action:    action,
		Scope:     folder,
		Effect:    effect,
		Params:    params,
		Predicate: predicate,
	}
	if err := s.RemoveACLRow(row); err != nil {
		slog.Warn("grants revoke: delete", "folder", folder, "err", err)
		http.Error(w, "delete failed", http.StatusInternalServerError)
		return
	}
	slog.Info("grant revoked", "folder", folder, "principal", principal, "action", action, "sub", sub)
	http.Redirect(w, r, "/dash/groups/"+folderPath(folder)+"/grants", http.StatusSeeOther)
}
