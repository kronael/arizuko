package main

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/kronael/arizuko/container"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/groupfolder"
	"github.com/kronael/arizuko/store"
)

// GET /dash/groups/new — folder + product form.
func (d *dash) handleGroupNewForm(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireUser(w, r); !ok {
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	pageTop(w, "New group")
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

// GET /dash/groups/{folder}/settings — show current state.
// POST persists. Cross-folder ambient (open + observe_window_*) helpers
// from spec 6/F aren't on Store yet; render the form disabled with a
// notice when the columns are absent.
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
	pageTop(w, "Settings — "+folder)

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

	open, owMsgs, owChars, hasAmbient := readAmbientSettings(d, folder)
	notice := ""
	disabledAttr := ""
	if !hasAmbient {
		notice = `<div class="banner-warn">Cross-folder ambient settings (open / observe_window_messages / observe_window_chars) ship pending — spec 6/F. Form disabled.</div>`
		disabledAttr = " disabled"
	}

	fmt.Fprintf(w, `<p class="dim">Group <code>%s</code> &middot; product <code>%s</code></p>`, esc(folder), esc(product))
	fmt.Fprint(w, notice)
	openChecked := ""
	if open {
		openChecked = " checked"
	}
	fmt.Fprintf(w, `<form method="post" action="/dash/groups/%s/settings">
<p><label><input type="checkbox" name="open" value="1"%s%s> open (allow cross-folder ambient observation)</label></p>
<p><label>observe_window_messages <input type="number" name="observe_window_messages" value="%d"%s min="0"></label></p>
<p><label>observe_window_chars <input type="number" name="observe_window_chars" value="%d"%s min="0"></label></p>
<p><button type="submit"%s>save</button></p>
</form>`, esc(folder), openChecked, disabledAttr, owMsgs, disabledAttr, owChars, disabledAttr, disabledAttr)

	fmt.Fprintf(w, `<h2>Danger zone</h2>
<form method="post" action="/dash/groups/%s/delete" onsubmit="return confirm('Delete group %s? Routes, sessions, files remain on disk; the DB row is removed.')">
<button type="submit" style="color:#b00">delete group</button>
</form>`, esc(folder), esc(folder))

	fmt.Fprint(w, pageBot)
}

// readAmbientSettings: returns open/observe-window fields when the
// schema supports them, false in hasAmbient if columns are missing.
// Spec 6/F (cross-folder ambient) hasn't landed; this stays soft so
// the page renders today and lights up the day the columns appear.
func readAmbientSettings(d *dash, folder string) (open bool, owMsgs, owChars int, hasAmbient bool) {
	row := d.dbRW.QueryRow(
		`SELECT COALESCE(open,0), COALESCE(observe_window_messages,0), COALESCE(observe_window_chars,0)
		 FROM groups WHERE folder = ?`, folder)
	if err := row.Scan(&open, &owMsgs, &owChars); err != nil {
		// Either no row (handled above) or missing columns — treat as unsupported.
		return false, 0, 0, false
	}
	return open, owMsgs, owChars, true
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
	// Reject save if ambient columns aren't there; form is disabled in the
	// happy path anyway, but a hand-crafted POST shouldn't silently no-op.
	if _, _, _, has := readAmbientSettings(d, folder); !has {
		http.Error(w, "ambient settings feature ship pending", http.StatusServiceUnavailable)
		return
	}
	open := r.FormValue("open") == "1"
	owMsgs, _ := strconv.Atoi(strings.TrimSpace(r.FormValue("observe_window_messages")))
	owChars, _ := strconv.Atoi(strings.TrimSpace(r.FormValue("observe_window_chars")))
	if _, err := d.dbRW.Exec(
		`UPDATE groups SET open = ?, observe_window_messages = ?, observe_window_chars = ?
		 WHERE folder = ?`,
		open, owMsgs, owChars, folder); err != nil {
		slog.Warn("group settings save", "folder", folder, "err", err)
		http.Error(w, "write failed", http.StatusInternalServerError)
		return
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
