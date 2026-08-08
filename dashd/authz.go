package main

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"slices"
	"strings"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/groupfolder"
	"github.com/kronael/arizuko/store"
)

// callerGroups parses the proxyd-signed X-User-Groups header into the list of
// folders the caller holds an allow-grant on (auth.UserScopes output). JSON
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
		for p := range strings.SplitSeq(hdr, ",") {
			if p = strings.TrimSpace(p); p != "" {
				groups = append(groups, p)
			}
		}
	}
	return groups
}

// callerScope returns the caller's allowed folders and whether the caller is an
// operator (`**` in the set → sees everything). Source of truth is the same the
// write gate trusts: the proxyd-signed X-User-Groups header (= auth.UserScopes
// at sign time), with an auth.UserScopes(sub) fallback when the header is absent
// (server-to-server callers, tests that seed only the ACL). ALL read handlers
// route their scope decision through this one helper — no per-handler
// re-derivation.
func (d *dash) callerScope(r *http.Request) (allowed []string, operator bool) {
	allowed = callerGroups(r)
	if len(allowed) == 0 {
		if sub := strings.TrimSpace(r.Header.Get("X-User-Sub")); sub != "" && d.adminDB() != nil {
			allowed = auth.UserScopes(store.New(d.adminDB()), strings.TrimPrefix(sub, "user:"))
		}
	}
	if slices.Contains(allowed, "**") {
		return allowed, true
	}
	return allowed, false
}

// visible reports whether a non-operator caller may see `folder`. Operators see
// everything; everyone else needs a scope covering the folder, decided by the
// one containment rule (auth.MatchGroups). Subtree reach is the grant's job —
// `corp/eng/**` reaches `corp/eng/sre`, a bare `corp/eng` does not. dashd used
// to walk folder prefixes here, which handed the dashboard a wider rule than
// auth.Authorize gives every other surface.
func visible(allowed []string, operator bool, folder string) bool {
	if operator {
		return true
	}
	if folder == "" {
		return false
	}
	return auth.MatchGroups(allowed, folder)
}

// requireVisible gates a per-folder GET to a non-operator caller. Operators
// pass; others must hold a grant covering `folder` (direct or subtree) or get
// 403 — never render another tenant's data. Returns false if the handler
// should abort (after writing the 403).
func (d *dash) requireVisible(w http.ResponseWriter, r *http.Request, folder string) bool {
	if _, ok := requireUser(w, r); !ok {
		return false
	}
	if d.adminDB() == nil {
		http.Error(w, "backend unavailable", http.StatusServiceUnavailable)
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
// visibility filtering. web:/hook: JIDs carry the folder in the prefix
// (groupfolder.JidFolder); any other platform JID resolves through the routes
// table (store.DefaultFolderForJID). Empty when no folder can be determined —
// such a message is invisible to every non-operator (fail-closed).
func (d *dash) jidFolder(jid string) string {
	if strings.HasPrefix(jid, "web:") || strings.HasPrefix(jid, "hook:") {
		return groupfolder.JidFolder(jid)
	}
	// No routd.db → no routes table to resolve through. Unresolvable, not a
	// panic: pages that render JIDs off HTTP-only data (approvals) reach here
	// with a nil handle.
	if d.adminDB() == nil {
		return ""
	}
	return store.New(d.adminDB()).DefaultFolderForJID(jid)
}

// countVisible is the one scope-filtered counter behind the portal/status
// numbers: operators get countQ's raw COUNT(*); scoped callers get listQ's
// single column mapped to a folder via toFolder and filtered through
// `visible`. Three counts, one shape — the per-count copies drifted before.
func (d *dash) countVisible(allowed []string, operator bool, countQ, listQ string, toFolder func(string) string) int {
	if d.adminDB() == nil {
		return 0
	}
	if operator {
		var n int
		if err := d.adminDB().QueryRow(countQ).Scan(&n); err != nil {
			slog.Warn("scope: count", "q", countQ, "err", err)
		}
		return n
	}
	rows, err := d.adminDB().Query(listQ)
	if err != nil {
		slog.Warn("scope: list", "q", listQ, "err", err)
		return 0
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			continue
		}
		if visible(allowed, operator, toFolder(v)) {
			n++
		}
	}
	return n
}

func folderIdentity(f string) string { return f }

// countVisibleGroups counts groups the caller may see.
func (d *dash) countVisibleGroups(allowed []string, operator bool) int {
	return d.countVisible(allowed, operator,
		`SELECT COUNT(*) FROM groups`,
		`SELECT folder FROM groups ORDER BY folder LIMIT 500`,
		folderIdentity)
}

// countVisibleErroredChats counts distinct errored chat_jids whose
// routing-target folder the caller may see.
func (d *dash) countVisibleErroredChats(allowed []string, operator bool) int {
	return d.countVisible(allowed, operator,
		`SELECT COUNT(DISTINCT chat_jid) FROM messages WHERE errored=1`,
		`SELECT DISTINCT chat_jid FROM messages WHERE errored=1 LIMIT 1000`,
		d.jidFolder)
}

// countVisibleFailedTasks counts task runs that errored in the last day whose
// owning group folder the caller may see.
func (d *dash) countVisibleFailedTasks(allowed []string, operator bool) int {
	return d.countVisible(allowed, operator,
		`SELECT COUNT(*) FROM task_run_logs WHERE status='error' AND run_at > datetime('now','-1 day')`,
		`SELECT t.owner FROM task_run_logs l JOIN scheduled_tasks t ON t.id = l.task_id
		 WHERE l.status='error' AND l.run_at > datetime('now','-1 day') LIMIT 1000`,
		folderIdentity)
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
	// ES256 subs carry a "user:" prefix; acl_membership keys on bare subs.
	sub = strings.TrimPrefix(sub, "user:")
	if d.adminDB() == nil {
		http.Error(w, "backend unavailable", http.StatusServiceUnavailable)
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

// callerAdmins is the non-writing twin of requireAdmin: it reports the same
// admin decision without emitting a 403. Read views use it to gate exposure of
// write-capable data (raw chat tokens), so a caller with only read visibility
// on a folder never sees a reusable bearer for it. Operators (`**` header) pass
// via the same shortcut callerScope uses; everyone else needs an admin grant.
func (d *dash) callerAdmins(r *http.Request, folder string) bool {
	if _, operator := d.callerScope(r); operator {
		return true
	}
	if d.adminDB() == nil {
		return false
	}
	sub := strings.TrimPrefix(strings.TrimSpace(r.Header.Get("X-User-Sub")), "user:")
	if sub == "" {
		return false
	}
	caller := auth.Caller{Principal: sub, Extra: callerGroups(r)}
	return auth.Authorize(store.New(d.adminDB()), caller, "admin", folder, nil)
}
