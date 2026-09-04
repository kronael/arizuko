package main

import (
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

// callerScope returns the caller's allowed folders and whether the caller is an
// operator (`**` in the set). Source of truth is the same the write gate trusts: the
// proxyd-signed X-User-Groups header, with an auth.UserScopes(sub) fallback when it is
// absent. ALL read handlers route their scope decision through this one helper.
func (d *dash) callerScope(r *http.Request) (allowed []string, operator bool) {
	allowed = auth.CallerGroups(r)
	if len(allowed) == 0 {
		if sub := strings.TrimSpace(r.Header.Get("X-User-Sub")); sub != "" && d.adminDB() != nil {
			scopes, err := auth.UserScopes(store.New(d.adminDB()), strings.TrimPrefix(sub, "user:"))
			if err != nil {
				// Fail closed — but say so. An unreadable policy and a user with no
				// grants both render as an empty dashboard (BUGS A4).
				slog.Error("dashd: caller scope read failed; treating as no access",
					"sub", sub, "err", err)
				return nil, false
			}
			allowed = scopes
		}
	}
	return allowed, slices.Contains(allowed, auth.OperatorGroup)
}

// visible reports whether a non-operator caller may see `folder`. Operators see
// everything; everyone else needs a scope covering the folder, decided by the one
// containment rule (auth.MatchGroups). Subtree reach is the grant's job. dashd used to
// walk folder prefixes here, which handed the dashboard a wider rule than
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
		auth.Forbidden(w, "dashd", "You do not have access to "+folder+".")
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
		auth.Forbidden(w, "dashd", "Operator access required.")
		return false
	}
	return true
}

// jidFolder maps a message chat_jid to its routing-target folder for visibility
// filtering. A web:/hook: JID resolves through the folder its mint RECORDED, because
// the JID grammar reads `hook:a/b/c` as folder `a/b` and would show tenant `a`'s
// message to tenant `a/b` (BUGS L4); the grammar stays the fallback for a token minted
// before the column. Empty when no folder can be determined — such a message is
// invisible to every non-operator.
func (d *dash) jidFolder(jid string) string {
	// No routd.db → no route_tokens or routes table to resolve through.
	// Unresolvable, not a panic: pages that render JIDs off HTTP-only data
	// (approvals) reach here with a nil handle.
	if d.adminDB() == nil {
		return ""
	}
	if strings.HasPrefix(jid, "web:") || strings.HasPrefix(jid, "hook:") {
		target, err := store.RouteTokenTargetByJID(d.adminDB(), jid)
		if err != nil {
			// Fail closed: the grammar's answer can name another tenant, and
			// this gate decides who sees the message.
			slog.Error("route token target read failed; message stays invisible",
				"jid", jid, "err", err)
			return ""
		}
		if target != "" {
			return target
		}
		return groupfolder.JidFolder(jid)
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

// requireAdmin gates a write to scope (folder or "**" for global) via auth.Authorize
// with action="admin"; deny-wins, no tier-default fallback. Caller principal is
// X-User-Sub; X-User-Groups folds in as Extra principals so an operator with `**` admin
// on a parent folder authorizes a child write. Returns false if the handler must abort.
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
	caller := auth.Caller{Principal: sub, Extra: auth.CallerGroups(r)}
	if !auth.Authorize(s, caller, "admin", scope, nil) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return "", false
	}
	return sub, true
}
