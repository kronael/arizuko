package main

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/kronael/arizuko/container"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/groupfolder"
	"github.com/kronael/arizuko/store"
)

var skillNameRe = regexp.MustCompile(`^[a-z0-9-]+$`)

// GET /dash/groups/new — folder + product form.
func (d *dash) handleGroupNewForm(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireUser(w, r); !ok {
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	pageTopFor(w, r, "New group",
		struct{ Href, Label string }{"/dash/groups/", "Groups"},
		struct{ Href, Label string }{"", "New"},
	)
	fmt.Fprint(w, `<p class="dim">Create a group folder. Use <code>parent/child</code> to nest.</p>
<form method="post" action="/dash/groups/new">
<p><label>Folder <input type="text" name="folder" placeholder="solo/inbox" required size="40"></label></p>
<p><label>Product <select name="product">
<option value="assistant">assistant (default)</option>
<option value="oracle">oracle</option>
</select></label></p>
<p><button type="submit">create</button></p>
</form>
<p class="dim">The folder skeleton (skills, settings, default tasks) is seeded via <code>container.SetupGroup</code>; admin is granted to the creator.</p>`)
	fmt.Fprint(w, pageBot)
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
	if d.dbRW == nil {
		http.Error(w, "store unavailable", http.StatusServiceUnavailable)
		return
	}

	var exists int
	if err := d.dbRW.QueryRow(`SELECT COUNT(*) FROM groups WHERE folder = ?`, folder).Scan(&exists); err != nil {
		slog.Warn("group create: exists check", "folder", folder, "err", err)
		http.Error(w, "check failed", http.StatusInternalServerError)
		return
	}
	if exists > 0 {
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
	s := store.New(d.dbRW)
	if err := s.PutGroup(core.Group{
		Folder: folder, AddedAt: time.Now(), Product: product,
	}); err != nil {
		slog.Error("group create: insert", "folder", folder, "err", err)
		http.Error(w, "insert failed", http.StatusInternalServerError)
		return
	}
	if err := s.SeedDefaultTasks(folder, folder); err != nil {
		slog.Warn("group create: seed tasks", "folder", folder, "err", err)
	}
	// Grant admin to creator on the new folder.
	if _, err := d.dbRW.Exec(`INSERT OR IGNORE INTO acl
		(principal, action, scope, effect, params, predicate, granted_at, granted_by)
		VALUES (?, 'admin', ?, 'allow', '', '', datetime('now'), 'dashd')`,
		sub, folder); err != nil {
		slog.Warn("group create: grant admin", "folder", folder, "sub", sub, "err", err)
	}
	slog.Info("group created", "folder", folder, "product", product, "sub", sub)
	http.Redirect(w, r, "/dash/groups/", http.StatusSeeOther)
}

// stockSkills returns the sorted list of skill names from ant/skills/ in appDir.
// Returns nil when appDir is empty or the directory can't be read.
func (d *dash) stockSkills() []string {
	if d.appDir == "" {
		return nil
	}
	dir := filepath.Join(d.appDir, "ant", "skills")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() && skillNameRe.MatchString(e.Name()) {
			names = append(names, e.Name())
		}
	}
	return names
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
	if _, ok := requireUser(w, r); !ok {
		return
	}
	folder := r.PathValue("folder")
	if folder == "" {
		http.Error(w, "bad folder", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	pageTopFor(w, r, "Settings — "+folder,
		struct{ Href, Label string }{"/dash/groups/", "Groups"},
		struct{ Href, Label string }{"", folder},
		struct{ Href, Label string }{"", "Settings"},
	)

	if d.dbRW == nil {
		fmt.Fprint(w, `<div class="banner-err">store unavailable</div>`)
		fmt.Fprint(w, pageBot)
		return
	}
	var product string
	err := d.dbRW.QueryRow(`SELECT product FROM groups WHERE folder = ?`, folder).Scan(&product)
	if err != nil {
		fmt.Fprintf(w, `<div class="banner-err">group not found: %s</div>`, esc(err.Error()))
		fmt.Fprint(w, pageBot)
		return
	}

	s := store.New(d.dbRW)
	open := s.IsGroupOpen(folder)
	owMsgs, owChars := s.GroupObserveWindow(folder)
	if owMsgs < 0 {
		owMsgs = 0
	}
	if owChars < 0 {
		owChars = 0
	}

	fmt.Fprintf(w, `<p class="dim">Group <code>%s</code> &middot; product <code>%s</code></p>`, esc(folder), esc(product))
	openChecked := ""
	if open {
		openChecked = " checked"
	}
	skills := d.stockSkills()
	disabled := d.skillsDisabled(folder)

	fmt.Fprintf(w, `<form method="post" action="/dash/groups/%s/settings">
<p><label><input type="checkbox" name="open" value="1"%s> open (allow cross-folder ambient observation)</label></p>
<p><label>observe_window_messages <input type="number" name="observe_window_messages" value="%d" min="0"></label></p>
<p><label>observe_window_chars <input type="number" name="observe_window_chars" value="%d" min="0"></label></p>
`, esc(folder), openChecked, owMsgs, owChars)

	if len(skills) > 0 {
		fmt.Fprint(w, `<h2>Skills</h2><p class="dim">Unchecked skills are disabled on next agent run.</p><ul style="list-style:none;padding:0">`)
		for _, name := range skills {
			checked := ""
			if !disabled[name] {
				checked = " checked"
			}
			fmt.Fprintf(w, `<li><label><input type="checkbox" name="skill_enabled" value="%s"%s> %s</label></li>`,
				esc(name), checked, esc(name))
		}
		fmt.Fprint(w, `</ul>`)
	}

	fmt.Fprint(w, `<p><button type="submit">save</button></p></form>`)

	fmt.Fprintf(w, `<h2>Danger zone</h2>
<form method="post" action="/dash/groups/%s/delete" onsubmit="return confirm('Delete group %s? Routes, sessions, files remain on disk; the DB row is removed.')">
<button type="submit" style="color:#b00">delete group</button>
</form>`, esc(folder), esc(folder))

	fmt.Fprint(w, pageBot)
}

func (d *dash) handleGroupSettingsSave(w http.ResponseWriter, r *http.Request) {
	folder := r.PathValue("folder")
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
	owMsgs, _ := strconv.Atoi(strings.TrimSpace(r.FormValue("observe_window_messages")))
	owChars, _ := strconv.Atoi(strings.TrimSpace(r.FormValue("observe_window_chars")))
	s := store.New(d.dbRW)
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

	// Skills: checked values are enabled; unchecked skills (all stock minus checked) get .disabled.
	enabledSet := make(map[string]bool)
	for _, v := range r.Form["skill_enabled"] {
		if skillNameRe.MatchString(v) {
			enabledSet[v] = true
		}
	}
	for _, name := range d.stockSkills() {
		disable := !enabledSet[name]
		if err := d.setSkillDisabled(folder, name, disable); err != nil {
			slog.Warn("group settings save: skill", "folder", folder, "skill", name, "err", err)
		}
	}

	slog.Info("group settings saved", "folder", folder)
	http.Redirect(w, r, "/dash/groups/"+folder+"/settings", http.StatusSeeOther)
}

// DELETE /dash/groups/{folder} (or POST .../delete from the form).
// Removes the DB row + best-effort rm of the groups/<folder>/ dir.
func (d *dash) handleGroupDelete(w http.ResponseWriter, r *http.Request) {
	folder := r.PathValue("folder")
	if folder == "" {
		http.Error(w, "bad folder", http.StatusBadRequest)
		return
	}
	if !groupfolder.IsValidFolder(folder) {
		http.Error(w, "bad folder", http.StatusBadRequest)
		return
	}
	if _, ok := d.requireAdmin(w, r, folder); !ok {
		return
	}
	if d.dbRW == nil {
		http.Error(w, "store unavailable", http.StatusServiceUnavailable)
		return
	}
	res, err := d.dbRW.Exec(`DELETE FROM groups WHERE folder = ?`, folder)
	if err != nil {
		slog.Warn("group delete: db", "folder", folder, "err", err)
		http.Error(w, "delete failed", http.StatusInternalServerError)
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		http.Error(w, "not found", http.StatusNotFound)
		return
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
	if r.Method == http.MethodPost {
		http.Redirect(w, r, "/dash/groups/", http.StatusSeeOther)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
