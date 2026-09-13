package main

import (
	"context"
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
		if sub := strings.TrimSpace(r.Header.Get("X-User-Sub")); sub != "" && d.routdStore() != nil {
			scopes, err := auth.UserScopes(d.routdStore(), strings.TrimPrefix(sub, "user:"))
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
	if d.routdStore() == nil {
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
	if d.routdStore() == nil {
		return ""
	}
	if strings.HasPrefix(jid, "web:") || strings.HasPrefix(jid, "hook:") {
		target, err := store.RouteTokenTargetByJID(
			context.Background(), d.routd, jid)
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
	return d.routdStore().DefaultFolderForJID(jid)
}

// countVisible is the one scope-filtered counter behind the portal/status
// numbers: an operator gets the store's raw total; a scoped caller gets the
// folder-bearing column mapped through toFolder and filtered through `visible`.
// Three counts, one shape — the per-count copies drifted before. Both halves
// come from dashstore as a pair, so a number and its scoped form are the same
// question asked twice rather than two questions.
func (d *dash) countVisible(name string, allowed []string, operator bool,
	total func() (int, error), each func() ([]string, error), toFolder func(string) string) int {
	if d.ds() == nil {
		return 0
	}
	if operator {
		n, err := total()
		if err != nil {
			slog.Warn("scope: count", "count", name, "err", err)
		}
		return n
	}
	vals, err := each()
	if err != nil {
		slog.Warn("scope: list", "count", name, "err", err)
	}
	n := 0
	for _, v := range vals {
		if visible(allowed, operator, toFolder(v)) {
			n++
		}
	}
	return n
}

func folderIdentity(f string) string { return f }

// countVisibleGroups counts groups the caller may see.
func (d *dash) countVisibleGroups(allowed []string, operator bool) int {
	st := d.ds()
	return d.countVisible("groups", allowed, operator,
		st.GroupCount, st.GroupFolders, folderIdentity)
}

// countVisibleErroredChats counts distinct errored chat_jids whose
// routing-target folder the caller may see.
func (d *dash) countVisibleErroredChats(allowed []string, operator bool) int {
	st := d.ds()
	return d.countVisible("errored-chats", allowed, operator,
		st.ErroredChatCount, st.ErroredChatJIDs, d.jidFolder)
}

// countVisibleFailedTasks counts task runs that errored in the last day whose
// owning group folder the caller may see.
func (d *dash) countVisibleFailedTasks(allowed []string, operator bool) int {
	st := d.ds()
	return d.countVisible("failed-tasks", allowed, operator,
		st.FailedTaskRunCount, st.FailedTaskOwners, folderIdentity)
}

// reviewerSub is the proxyd-verified human behind this request, in the spelling
// acl rows carry: ES256 subs arrive with a "user:" prefix and acl_membership
// keys on the bare sub. Empty when the request carries no verified identity.
func reviewerSub(r *http.Request) string {
	return strings.TrimPrefix(strings.TrimSpace(r.Header.Get("X-User-Sub")), "user:")
}

// adminOn reports whether the caller holds `admin` over scope (a folder, or "**"
// for global) — the ONE authority question dashd asks, so a page that only
// COUNTS what a caller may act on asks it the same way the write gate does.
// X-User-Groups folds in as Extra principals so an operator with `**` admin on a
// parent folder authorizes a child write. No routd.db, no answer: false.
func (d *dash) adminOn(r *http.Request, scope string) bool {
	if d.routdStore() == nil {
		return false
	}
	caller := auth.Caller{Principal: reviewerSub(r), Extra: auth.CallerGroups(r)}
	return auth.Authorize(d.routdStore(), caller, "admin", scope, nil)
}

// requireAdmin gates a write to scope via adminOn; deny-wins, no tier-default
// fallback. Returns false if the handler must abort.
func (d *dash) requireAdmin(w http.ResponseWriter, r *http.Request, scope string) (string, bool) {
	sub, ok := requireUser(w, r)
	if !ok {
		return "", false
	}
	if !requireSameOrigin(w, r) {
		return "", false
	}
	if d.routdStore() == nil {
		http.Error(w, "backend unavailable", http.StatusServiceUnavailable)
		return "", false
	}
	if !d.adminOn(r, scope) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return "", false
	}
	return strings.TrimPrefix(sub, "user:"), true
}
