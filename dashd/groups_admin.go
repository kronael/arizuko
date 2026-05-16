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
	fmt.Fprintf(w, `<form method="post" action="/dash/groups/%s/settings">
<p><label><input type="checkbox" name="open" value="1"%s> open (allow cross-folder ambient observation)</label></p>
<p><label>observe_window_messages <input type="number" name="observe_window_messages" value="%d" min="0"></label></p>
<p><label>observe_window_chars <input type="number" name="observe_window_chars" value="%d" min="0"></label></p>
<p><button type="submit">save</button></p>
</form>`, esc(folder), openChecked, owMsgs, owChars)

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
