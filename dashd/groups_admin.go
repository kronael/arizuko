package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/kronael/arizuko/audit"
	"github.com/kronael/arizuko/container"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/dashd/dashstore"
	"github.com/kronael/arizuko/groupfolder"
	"github.com/kronael/arizuko/resreg/resources"
)

var skillNameRe = regexp.MustCompile(`^[a-z0-9-]+$`)

// numOrBlank renders a settings value as a string, or "" when ≤0 (unset) so the
// form shows an empty field rather than a 0 that would persist as a real override.
func numOrBlank(n int) string {
	if n <= 0 {
		return ""
	}
	return strconv.Itoa(n)
}

// clearableNum parses an observe-window form field, returning -1 (the
// SetGroupObserveWindow clear sentinel) for a blank, zero, or invalid value and
// the positive value otherwise.
func clearableNum(raw string) int {
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || n <= 0 {
		return -1
	}
	return n
}

// GET /dash/groups/new — folder + product form.
func (d *dash) handleGroupNewForm(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireUser(w, r); !ok {
		return
	}
	pageTopFor(w, r, "New group",
		crumb{"/dash/groups/", "Groups"},
		crumb{"", "New"},
	)
	_, _ = fmt.Fprint(w, `<p class="dim">Create a group folder. Use <code>parent/child</code> to nest.</p>
<form method="post" action="/dash/groups/new">
<p><label>Folder <input type="text" name="folder" placeholder="solo/inbox" required size="40"></label></p>
<p><label>Product <select name="product">
<option value="assistant">assistant (default)</option>
<option value="oracle">oracle</option>
</select></label></p>
<p><button type="submit">create</button></p>
</form>
<p class="dim">Creating a group creates its folder and grants you admin. The dashboard also attempts to seed default tasks.</p>`)
	pageClose(w, r)
}

// POST /dash/groups/new — actually create.
func (d *dash) handleGroupCreate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	folder := strings.TrimSpace(r.FormValue("folder"))
	product := strings.TrimSpace(r.FormValue("product"))
	if product == "" {
		product = core.DefaultProduct
	}
	if folder == "" {
		http.Error(w, "folder: empty", http.StatusBadRequest)
		return
	}
	if !groupfolder.IsValidFolder(folder) {
		http.Error(w, "folder: invalid", http.StatusBadRequest)
		return
	}
	// Admin scope = the target folder itself (creator must already hold
	// admin on the prefix, e.g. `**` operator or admin on the parent).
	sub, ok := d.requireAdmin(w, r, folder)
	if !ok {
		return
	}
	if d.routdStore() == nil {
		http.Error(w, "store unavailable", http.StatusServiceUnavailable)
		return
	}

	exists, err := d.ds().GroupExists(folder)
	if err != nil {
		slog.Warn("group create: exists check", "folder", folder, "err", err)
		http.Error(w, "check failed", http.StatusInternalServerError)
		return
	}
	if exists {
		http.Error(w, "folder already exists", http.StatusConflict)
		return
	}

	cfg, err := core.LoadConfig()
	if err != nil {
		slog.Error("group create: load config", "err", err)
		http.Error(w, "config load failed", http.StatusInternalServerError)
		return
	}
	if err := container.SetupGroup(cfg, folder, ""); err != nil {
		slog.Error("group create: setup", "folder", folder, "err", err)
		http.Error(w, "setup failed", http.StatusInternalServerError)
		return
	}
	ev := audit.Event{
		Category: audit.CategoryMutation,
		Action:   "group.create",
		Actor:    "user:" + sub,
		ActorSub: sub,
		Surface:  audit.SurfaceREST,
		Resource: "groups/" + folder,
		Folder:   folder,
		Outcome:  audit.OutcomeOK,
		ParamsSummary: map[string]any{
			"folder":  folder,
			"product": product,
		},
	}
	switch err := d.ds().CreateGroupWithAdmin(r.Context(), folder, product, sub, ev); {
	case errors.Is(err, dashstore.ErrSeedMemberRole):
		slog.Error("group create: seed member role", "folder", folder, "err", err)
		http.Error(w, "seed member role failed", http.StatusInternalServerError)
		return
	case errors.Is(err, dashstore.ErrGrantAdmin):
		slog.Error("group create: grant admin", "folder", folder, "sub", sub, "err", err)
		http.Error(w, "grant admin failed", http.StatusInternalServerError)
		return
	case err != nil:
		slog.Error("group create: insert", "folder", folder, "err", err)
		http.Error(w, "insert failed", http.StatusInternalServerError)
		return
	}
	if err := d.routdStore().SeedDefaultTasks(folder, folder); err != nil {
		slog.Warn("group create: seed tasks", "folder", folder, "err", err)
	}
	slog.Info("group created", "folder", folder, "product", product, "sub", sub)
	http.Redirect(w, r, "/dash/groups/", http.StatusSeeOther)
}

// stockSkills returns the sorted list of skill names from ant/skills/ in appDir.
// Returns nil when appDir is empty or the directory can't be read.
func (d *dash) stockSkills(folder string) ([]string, error) {
	selected, err := d.routdStore().SelectedPackageNames(folder)
	if err != nil {
		return nil, err
	}
	inventory, err := container.ReadPackageInventory(filepath.Dir(d.groupsDir), selected)
	var names []string
	for _, skill := range inventory.Skills {
		names = append(names, skill.Name)
	}
	return names, err
}

// skillsDisabled returns the set of skill names that have a .disabled marker
// under a group's .claude/skills/ dir.
func (d *dash) skillsDisabled(folder string) map[string]bool {
	base := filepath.Join(d.groupsDir, filepath.Clean(folder), ".claude", "skills")
	entries, err := os.ReadDir(base)
	if err != nil {
		return nil
	}
	out := make(map[string]bool)
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		marker := filepath.Join(base, e.Name(), ".disabled")
		if _, err := os.Stat(marker); err == nil {
			out[e.Name()] = true
		}
	}
	return out
}

// setSkillDisabled creates or removes the .disabled marker for one skill.
func (d *dash) setSkillDisabled(folder, skill string, disable bool) error {
	if !skillNameRe.MatchString(skill) {
		return fmt.Errorf("invalid skill name: %s", skill)
	}
	dir := filepath.Join(d.groupsDir, filepath.Clean(folder), ".claude", "skills", skill)
	marker := filepath.Join(dir, ".disabled")
	if disable {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		f, err := os.OpenFile(marker, os.O_CREATE|os.O_TRUNC, 0o644)
		if err != nil {
			return err
		}
		return f.Close()
	}
	err := os.Remove(marker)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// GET /dash/groups/{folder}/settings — show current state. POST persists.
func (d *dash) handleGroupSettings(w http.ResponseWriter, r *http.Request) {
	folder := groupFromPath(r, "/settings")
	if folder == "" {
		http.Error(w, "bad folder", http.StatusBadRequest)
		return
	}
	if !d.requireVisible(w, r, folder) {
		return
	}
	jid := strings.TrimSpace(r.URL.Query().Get("jid"))
	var chat *resources.ChatSettingsResponse
	if jid != "" {
		var ok bool
		chat, ok = d.readChatSettings(w, r, folder, jid)
		if !ok {
			return
		}
	}
	pageTopFor(w, r, "Settings — "+folder,
		crumb{"/dash/groups/", "Groups"},
		crumb{"", folder},
		crumb{"", "Settings"},
	)

	if d.routdStore() == nil {
		_, _ = fmt.Fprint(w, htmlBanner("err", "store unavailable"))
		pageClose(w, r)
		return
	}
	// READ through routd's /v1 (spec 6/28 §"Read-path: /v1 only, no direct DB").
	// ONE call answers the whole page: the group row now carries `open` and the
	// observe windows alongside product, model and container_config, so this no
	// longer needs a query plus two accessors — and it never reads one row over
	// two transports, which is what stopped this page migrating before.
	//
	// The WRITES below still go through the store: they carry the operator onto
	// the audit row via AsUser and dashd's service bearer does not, so moving
	// them waits on 6/29's identity decision.
	g, err := d.getGroup(r.Context(), folder)
	if err != nil {
		_, _ = fmt.Fprint(w, htmlBanner("err", "could not read group: "+esc(err.Error())))
		pageClose(w, r)
		return
	}
	// g.Config (container_config) is not rendered on this page; the old code
	// unmarshalled it into a variable nothing read, which only compiled because
	// Unmarshal took its address.
	product, groupModel := g.Product, g.Model
	open := g.Open
	owMsgs, owChars := g.ObserveWindowMessages, g.ObserveWindowChars

	_, _ = fmt.Fprintf(w, `<p class="dim">Group <code>%s</code> &middot; product <code>%s</code></p>`, esc(folder), esc(product))
	openChecked := ""
	if open {
		openChecked = " checked"
	}
	skills, err := d.stockSkills(folder)
	if err != nil {
		_, _ = fmt.Fprint(w, htmlBanner("err", esc(err.Error())))
		pageClose(w, r)
		return
	}
	disabled := d.skillsDisabled(folder)

	type modelOption struct{ ID, Label string }
	modelOptions := []modelOption{
		{"", "instance default"},
		{"claude-opus-4-7", "Claude Opus 4.7"},
		{"claude-sonnet-4-6", "Claude Sonnet 4.6"},
		{"claude-haiku-4-5-20251001", "Claude Haiku 4.5"},
	}
	var modelSelect strings.Builder
	modelSelect.WriteString(`<select name="model">`)
	for _, opt := range modelOptions {
		sel := ""
		if opt.ID == groupModel {
			sel = " selected"
		}
		fmt.Fprintf(&modelSelect, `<option value="%s"%s>%s</option>`, esc(opt.ID), sel, esc(opt.Label))
	}
	modelSelect.WriteString(`</select>`)

	_, _ = fmt.Fprintf(w, `<form method="post" action="/dash/groups/%s/settings">`, folderPath(folder))
	_, _ = fmt.Fprint(w, htmlFormRow("Model", modelSelect.String()))
	_, _ = fmt.Fprintf(w, `<p><label><input type="checkbox" name="open" value="1"%s> open <span class="dim">— sibling groups can see messages sent here</span></label></p>`, openChecked)
	_, _ = fmt.Fprint(w, htmlFormRow("Messages a nearby group can peek at",
		fmt.Sprintf(`<input type="number" name="observe_window_messages" value="%s" min="0"> <span class="dim">max messages a sibling sees (blank = default 50)</span>`, numOrBlank(owMsgs))))
	_, _ = fmt.Fprint(w, htmlFormRow("observe_window_chars",
		fmt.Sprintf(`<input type="number" name="observe_window_chars" value="%s" min="0"> <span class="dim">max chars per observation (blank = default 2000)</span>`, numOrBlank(owChars))))
	_, _ = fmt.Fprintf(w, `<h2>Agent files</h2>`+
		`<p class="dim">Edit in the workspace browser — dufs opens text files in its built-in editor.</p>`+
		`<ul>`+
		`<li><a href="/dav/%s/SYSTEM.md" target="_blank">SYSTEM.md</a> — instructions read on every turn</li>`+
		`<li><a href="/dav/%s/CLAUDE.md" target="_blank">CLAUDE.md</a> — group policy and metadata</li>`+
		`<li><a href="/dav/%s/PERSONA.md" target="_blank">PERSONA.md</a> — name, tone, role</li>`+
		`<li><a href="/dav/%s/MEMORY.md" target="_blank">MEMORY.md</a> — persistent cross-session notes</li>`+
		`<li><a href="/dav/%s/" target="_blank">workspace/</a> — browse all group files</li>`+
		`</ul>`, folderPath(folder), folderPath(folder), folderPath(folder), folderPath(folder), folderPath(folder))

	if len(skills) > 0 {
		_, _ = fmt.Fprint(w, `<h2>Skills</h2><p class="dim">Unchecked skills are disabled on next agent run.</p><ul class="plain-list">`)
		for _, name := range skills {
			checked := ""
			if !disabled[name] {
				checked = " checked"
			}
			_, _ = fmt.Fprintf(w, `<li><label><input type="checkbox" name="skill_enabled" value="%s"%s> %s</label></li>`,
				esc(name), checked, esc(name))
		}
		_, _ = fmt.Fprint(w, `</ul>`)
	}

	_, _ = fmt.Fprint(w, `<p><button type="submit">save</button></p></form>`)
	_, _ = fmt.Fprintf(w, `<h2>Chat setup</h2><form method="get" action="/dash/groups/%s/settings"><label>Chat JID <input name="jid" value="%s" required></label> <button type="submit">load</button></form>`, folderPath(folder), esc(jid))
	if chat != nil {
		current := "off"
		if chat.ThreadReplies {
			current = "on"
		}
		_, _ = fmt.Fprintf(w, `<p>Thread replies: %s</p><form method="post" action="/dash/groups/%s/settings/chat"><input type="hidden" name="jid" value="%s"><button name="thread_replies" value="on">on</button> <button name="thread_replies" value="off">off</button> <button name="thread_replies" value="default">reset to default</button></form>`, current, folderPath(folder), esc(jid))
	}

	_, _ = fmt.Fprintf(w, `<p><a href="/dash/groups/%s/grants">Manage grants &rarr;</a></p>`, folderPath(folder))
	_, _ = fmt.Fprintf(w, `<p><a href="/dash/groups/%s/tools">Browse tools &rarr;</a></p>`, folderPath(folder))

	_, _ = fmt.Fprintf(w, `<details><summary>Danger zone</summary><div style="margin-top:.5rem">`+
		`<form method="post" action="/dash/groups/%s/delete" onsubmit="return confirm('Delete group %s? Routes, sessions, files remain on disk; the DB row is removed.')">`+
		`<button type="submit" class="btn-danger">delete group</button></form>`+
		`</div></details>`, folderPath(folder), esc(folder))

	pageClose(w, r)
}

func (d *dash) handleGroupSettingsSave(w http.ResponseWriter, r *http.Request) {
	folder := groupFromPath(r, "/settings")
	if folder == "" {
		http.Error(w, "bad folder", http.StatusBadRequest)
		return
	}
	if _, ok := d.requireAdmin(w, r, folder); !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	open := r.FormValue("open") == "1"
	// Blank or 0 = clear to default. SetGroupObserveWindow clears on <0, so map
	// an empty/zero submission to -1 instead of persisting 0 as a real override.
	owMsgs := clearableNum(r.FormValue("observe_window_messages"))
	owChars := clearableNum(r.FormValue("observe_window_chars"))
	model := r.FormValue("model")
	s := d.routdStore()
	if err := s.SetGroupOpen(folder, open); err != nil {
		slog.Warn("group settings save: open", "folder", folder, "err", err)
		http.Error(w, "write failed", http.StatusInternalServerError)
		return
	}
	if err := s.SetGroupObserveWindow(folder, owMsgs, owChars); err != nil {
		slog.Warn("group settings save: observe", "folder", folder, "err", err)
		http.Error(w, "write failed", http.StatusInternalServerError)
		return
	}
	if err := s.SetGroupModel(folder, model); err != nil {
		slog.Warn("group settings save: model", "folder", folder, "err", err)
		http.Error(w, "write failed", http.StatusInternalServerError)
		return
	}
	// Skills: checked values are enabled; unchecked skills (all stock minus checked) get .disabled.
	enabledSet := make(map[string]bool)
	for _, v := range r.Form["skill_enabled"] {
		if skillNameRe.MatchString(v) {
			enabledSet[v] = true
		}
	}
	skills, err := d.stockSkills(folder)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	for _, name := range skills {
		disable := !enabledSet[name]
		if err := d.setSkillDisabled(folder, name, disable); err != nil {
			slog.Warn("group settings save: skill", "folder", folder, "skill", name, "err", err)
		}
	}

	slog.Info("group settings saved", "folder", folder)
	http.Redirect(w, r, "/dash/groups/"+folderPath(folder)+"/settings", http.StatusSeeOther)
}

// DELETE /dash/groups/{folder} (or POST .../delete from the form).
// Removes the folder's subtree and everything scoped to it, stops each removed
// folder's run at runed, then does a best-effort rm of the groups/<folder>/ dir.
// runed unreachable -> 503 and nothing removed, because a live run could
// outlive the group; the operator's escape is `arizuko group <instance> rm -f`.
// A stop that fails after the purge is a 200 page with the reason: the rows
// are gone already.
func (d *dash) handleGroupDelete(w http.ResponseWriter, r *http.Request) {
	folder := groupFromPath(r, "/delete")
	if folder == "" {
		http.Error(w, "bad folder", http.StatusBadRequest)
		return
	}
	if !groupfolder.IsValidFolder(folder) {
		http.Error(w, "bad folder", http.StatusBadRequest)
		return
	}
	sub, ok := d.requireAdmin(w, r, folder)
	if !ok {
		return
	}
	if d.routdStore() == nil {
		http.Error(w, "store unavailable", http.StatusServiceUnavailable)
		return
	}
	runed := d.runedClient()
	if err := runed.Health(r.Context()); err != nil {
		slog.Warn("group delete: runed unreachable", "folder", folder, "runed", runed.BaseURL, "err", err)
		http.Error(w, fmt.Sprintf("Nothing was removed: cannot reach runed at %s (%v), so a live run "+
			"could outlive the group. Bring runed up, or stop the instance and run "+
			"`arizuko group <instance> rm -f %s`.", runed.BaseURL, err, folder), http.StatusServiceUnavailable)
		return
	}
	stop := func(ctx context.Context, f string) error {
		_, err := runed.StopFolder(ctx, f)
		return err
	}
	// store.DeleteGroupStopping is the ONE purge — what it removes is declared
	// once, in store.folderScopePurge — and AsUser attributes its audit row to
	// the operator who pressed the button. A second list here is a second thing
	// to keep in step, and two lists purging different halves is BUGS L198.
	removed, swept, err := d.routdStore().AsUser(sub).DeleteGroupStopping(r.Context(), folder, stop)
	// 404 only when NOTHING went. A folder whose groups row is already gone can
	// still own acl, secret, task and route rows, and deleting those is work: a
	// 404 there told the operator the rows are still there (BUGS L323).
	if removed == 0 && swept == 0 {
		if err != nil {
			slog.Warn("group delete: db", "folder", folder, "err", err)
			http.Error(w, "delete failed", http.StatusInternalServerError)
			return
		}
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if removed == 0 {
		slog.Info("group delete: swept orphan rows, no groups row",
			"folder", folder, "swept", swept)
	}
	if err != nil {
		slog.Warn("group delete: rows removed, run not stopped", "folder", folder, "err", err)
	}
	if d.groupsDir != "" {
		// Best-effort cleanup. Symlink-escape guard via filepath.Clean +
		// prefix check before rm.
		groupDir := filepath.Join(d.groupsDir, filepath.Clean(folder))
		if strings.HasPrefix(groupDir, d.groupsDir+string(filepath.Separator)) {
			if err := os.RemoveAll(groupDir); err != nil && !errors.Is(err, os.ErrNotExist) {
				slog.Warn("group delete: rm dir", "folder", folder, "err", err)
			}
		}
	}
	slog.Info("group deleted", "folder", folder)
	if err != nil {
		pageTopFor(w, r, "Group deleted", crumb{"/dash/groups/", "Groups"}, crumb{"", "Deleted"})
		_, _ = fmt.Fprint(w, htmlBanner("warn", "The rows of "+folder+" are removed, but a run may still be live: "+err.Error()))
		_, _ = fmt.Fprint(w, `<p><a href="/dash/groups/">Back to groups</a></p>`)
		pageClose(w, r)
		return
	}
	if r.Method == http.MethodPost {
		http.Redirect(w, r, "/dash/groups/", http.StatusSeeOther)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// getGroup reads one group from routd's /v1 face.
//
// The row decodes into core.Group — the same struct routd serves — so the page
// cannot render a field the API lacks, and its `open` / observe-window
// projections default exactly as IsGroupOpen and GroupObserveWindow do: a
// group with no key is OPEN and an unset window is -1, never 0.
func (d *dash) getGroup(ctx context.Context, folder string) (core.Group, error) {
	body, status, err := d.bearerCall(ctx, d.routdURL, http.MethodGet,
		"/v1/groups/"+url.PathEscape(folder), nil)
	if err != nil {
		return core.Group{}, err
	}
	if status < 200 || status >= 300 {
		return core.Group{}, fmt.Errorf("routd /v1/groups/%s: status %d", folder, status)
	}
	var g core.Group
	if err := json.Unmarshal(body, &g); err != nil {
		return core.Group{}, err
	}
	return g, nil
}
