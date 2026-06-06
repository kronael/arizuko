package main

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/store"
)

// callerGroups parses the proxyd-signed X-User-Groups header into the list of
// folders the caller holds an allow-grant on (store.UserScopes output). JSON
// array is canonical; a comma-separated fallback covers non-JSON encoders.
// This is the SAME extraction requireAdmin folds into auth.Authorize.
func callerGroups(r *http.Request) []string {
	hdr := r.Header.Get("X-User-Groups")
	if hdr == "" {
		return nil
	}
	var groups []string
	_ = json.Unmarshal([]byte(hdr), &groups)
	if len(groups) == 0 && !strings.HasPrefix(strings.TrimSpace(hdr), "[") {
		for _, p := range strings.Split(hdr, ",") {
			if p = strings.TrimSpace(p); p != "" {
				groups = append(groups, p)
			}
		}
	}
	return groups
}

// callerScope returns the caller's allowed folders and whether the caller is an
// operator (`**` in the set → sees everything). Source of truth is the same the
// write gate trusts: the proxyd-signed X-User-Groups header (= store.UserScopes
// at sign time), with a store.UserScopes(sub) fallback when the header is absent
// (server-to-server callers, tests that seed only the ACL). ALL read handlers
// route their scope decision through this one helper — no per-handler
// re-derivation.
func (d *dash) callerScope(r *http.Request) (allowed []string, operator bool) {
	allowed = callerGroups(r)
	if len(allowed) == 0 {
		if sub := strings.TrimSpace(r.Header.Get("X-User-Sub")); sub != "" {
			allowed = store.New(d.adminDB()).UserScopes(sub)
		}
	}
	for _, a := range allowed {
		if a == "**" {
			return allowed, true
		}
	}
	return allowed, false
}

// visible reports whether a non-operator caller may see `folder`. Operators see
// everything. Otherwise the folder must be granted directly (auth.MatchGroups —
// `**`/`*` patterns) OR be a subtree of a granted folder (allowed "corp/eng" →
// "corp/eng/sre"). MatchGroups requires equal segment depth for non-`**`
// patterns, so the subtree case is checked explicitly.
func visible(allowed []string, operator bool, folder string) bool {
	if operator {
		return true
	}
	if folder == "" {
		return false
	}
	if auth.MatchGroups(allowed, folder) {
		return true
	}
	for _, a := range allowed {
		if a == "" {
			continue
		}
		if folder == a || strings.HasPrefix(folder, a+"/") {
			return true
		}
	}
	return false
}

// requireVisible gates a per-folder GET to a non-operator caller. Operators
// pass; others must hold a grant covering `folder` (direct or subtree) or get
// 403 — never render another tenant's data. Returns false if the handler
// should abort (after writing the 403).
func (d *dash) requireVisible(w http.ResponseWriter, r *http.Request, folder string) bool {
	if _, ok := requireUser(w, r); !ok {
		return false
	}
	allowed, operator := d.callerScope(r)
	if !visible(allowed, operator, folder) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return false
	}
	return true
}

// requireOperator gates an instance-wide section (invites, channels) to an
// operator (`**`). Non-operators get 403; the matching nav links are also
// hidden. Returns false if the handler should abort.
func (d *dash) requireOperator(w http.ResponseWriter, r *http.Request) bool {
	if _, ok := requireUser(w, r); !ok {
		return false
	}
	if _, operator := d.callerScope(r); !operator {
		http.Error(w, "forbidden", http.StatusForbidden)
		return false
	}
	return true
}

// jidFolder maps a message chat_jid to its routing-target folder for
// visibility filtering. web:<folder>[/...] and hook:<folder>/... carry the
// folder in the prefix; any other platform JID resolves through the routes
// table (store.DefaultFolderForJID). Empty when no folder can be determined —
// such a message is invisible to every non-operator (fail-closed).
func (d *dash) jidFolder(jid string) string {
	if rest, ok := strings.CutPrefix(jid, "web:"); ok {
		return rest
	}
	if rest, ok := strings.CutPrefix(jid, "hook:"); ok {
		if i := strings.LastIndexByte(rest, '/'); i > 0 {
			return rest[:i]
		}
		return rest
	}
	return store.New(d.adminDB()).DefaultFolderForJID(jid)
}

// countVisibleGroups counts groups the caller may see. Operators get the raw
// COUNT(*); non-operators get the number of group folders that pass `visible`.
func (d *dash) countVisibleGroups(allowed []string, operator bool) int {
	if operator {
		var n int
		if err := d.adminDB().QueryRow(`SELECT COUNT(*) FROM groups`).Scan(&n); err != nil {
			slog.Warn("scope: group count", "err", err)
		}
		return n
	}
	rows, err := d.adminDB().Query(`SELECT folder FROM groups ORDER BY folder LIMIT 500`)
	if err != nil {
		slog.Warn("scope: group list", "err", err)
		return 0
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var f string
		if err := rows.Scan(&f); err != nil {
			continue
		}
		if visible(allowed, operator, f) {
			n++
		}
	}
	return n
}

// countVisibleErroredChats counts distinct errored chat_jids whose
// routing-target folder the caller may see. Operators get the raw distinct
// count.
func (d *dash) countVisibleErroredChats(allowed []string, operator bool) int {
	if operator {
		var n int
		if err := d.db.QueryRow(`SELECT COUNT(DISTINCT chat_jid) FROM messages WHERE errored=1`).Scan(&n); err != nil {
			slog.Warn("scope: errored count", "err", err)
		}
		return n
	}
	rows, err := d.db.Query(`SELECT DISTINCT chat_jid FROM messages WHERE errored=1 LIMIT 1000`)
	if err != nil {
		slog.Warn("scope: errored list", "err", err)
		return 0
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var jid string
		if err := rows.Scan(&jid); err != nil {
			continue
		}
		if visible(allowed, operator, d.jidFolder(jid)) {
			n++
		}
	}
	return n
}

// countVisibleFailedTasks counts task runs that errored in the last day whose
// owning group folder the caller may see. Operators get the raw count.
func (d *dash) countVisibleFailedTasks(allowed []string, operator bool) int {
	if operator {
		var n int
		if err := d.adminDB().QueryRow(
			`SELECT COUNT(*) FROM task_run_logs WHERE status='error' AND run_at > datetime('now','-1 day')`).Scan(&n); err != nil {
			slog.Warn("scope: failed-task count", "err", err)
		}
		return n
	}
	rows, err := d.adminDB().Query(
		`SELECT t.owner FROM task_run_logs l JOIN scheduled_tasks t ON t.id = l.task_id
		 WHERE l.status='error' AND l.run_at > datetime('now','-1 day') LIMIT 1000`)
	if err != nil {
		slog.Warn("scope: failed-task list", "err", err)
		return 0
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var owner string
		if err := rows.Scan(&owner); err != nil {
			continue
		}
		if visible(allowed, operator, owner) {
			n++
		}
	}
	return n
}

// requireAdmin gates a write to scope (folder or "**" for global). Uses
// auth.Authorize with action="admin"; deny-wins, tier-default fallback
// off (admin is not an mcp:* action). Caller principal is X-User-Sub
// (proxyd-verified); X-User-Groups folds in as Extra principals so an
// operator with `**` admin on a parent folder authorizes a child write.
// 403 + log on denial. Returns false if the handler should abort.
func (d *dash) requireAdmin(w http.ResponseWriter, r *http.Request, scope string) (string, bool) {
	sub, ok := requireUser(w, r)
	if !ok {
		return "", false
	}
	if !requireSameOrigin(w, r) {
		return "", false
	}
	s := store.New(d.adminDB())
	caller := auth.Caller{Principal: sub, Extra: callerGroups(r)}
	if !auth.Authorize(s, caller, "admin", scope, nil) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return "", false
	}
	return sub, true
}
