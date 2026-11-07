package main

import (
	"context"
	"log/slog"
	"net/http"
	"strings"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/groupfolder"
	"github.com/kronael/arizuko/store"
)

func (d *dash) callerScope(r *http.Request) (auth.Policy, bool) {
	if p, ok := r.Context().Value(policyCtxKey{}).(auth.Policy); ok {
		return p, p.Allows("admin", "**", nil)
	}
	if d.routdStore() == nil || reviewerSub(r) == "" {
		return auth.Policy{}, false
	}
	p, _, err := auth.ReadPolicy(d.routdStore(), auth.Caller{Principal: reviewerSub(r)})
	if err != nil {
		slog.Error("dashd: caller policy read failed", "err", err)
		return auth.Policy{}, false
	}
	return p, p.Allows("admin", "**", nil)
}

func visible(p auth.Policy, folder string) bool {
	return folder != "" && p.Allows("interact", folder, nil)
}

func (d *dash) requireVisible(w http.ResponseWriter, r *http.Request, folder string) bool {
	if _, ok := requireUser(w, r); !ok {
		return false
	}
	if d.routdStore() == nil {
		http.Error(w, "backend unavailable", http.StatusServiceUnavailable)
		return false
	}
	p, _ := d.callerScope(r)
	if !visible(p, folder) {
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

func (d *dash) countVisible(name string, p auth.Policy,
	each func() ([]string, error), toFolder func(string) string) int {
	if d.ds() == nil {
		return 0
	}
	vals, err := each()
	if err != nil {
		slog.Warn("scope: list", "count", name, "err", err)
	}
	n := 0
	for _, v := range vals {
		if visible(p, toFolder(v)) {
			n++
		}
	}
	return n
}

func folderIdentity(f string) string { return f }

// countVisibleGroups counts groups the caller may see.
func (d *dash) countVisibleGroups(p auth.Policy) int {
	st := d.ds()
	return d.countVisible("groups", p, st.GroupFolders, folderIdentity)
}

// countVisibleErroredChats counts distinct errored chat_jids whose
// routing-target folder the caller may see.
func (d *dash) countVisibleErroredChats(p auth.Policy) int {
	st := d.ds()
	return d.countVisible("errored-chats", p, st.ErroredChatJIDs, d.jidFolder)
}

// countVisibleFailedTasks counts task runs that errored in the last day whose
// owning group folder the caller may see.
func (d *dash) countVisibleFailedTasks(p auth.Policy) int {
	st := d.ds()
	return d.countVisible("failed-tasks", p, st.FailedTaskOwners, folderIdentity)
}

// reviewerSub is the proxyd-verified human behind this request, in the spelling
// acl rows carry: ES256 subs arrive with a "user:" prefix and acl_membership
// keys on the bare sub. Empty when the request carries no verified identity.
func reviewerSub(r *http.Request) string {
	return strings.TrimPrefix(strings.TrimSpace(r.Header.Get("X-User-Sub")), "user:")
}

func (d *dash) adminOn(r *http.Request, scope string) bool {
	if d.routdStore() == nil {
		return false
	}
	caller := auth.Caller{Principal: reviewerSub(r)}
	return auth.Authorize(d.routdStore(), caller, "admin", scope, nil)
}

func (d *dash) requireWriter(w http.ResponseWriter, r *http.Request) (string, bool) {
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
	return strings.TrimPrefix(sub, "user:"), true
}

// requireAdmin gates a write to scope via adminOn; deny-wins, no tier-default
// fallback. Returns false if the handler must abort.
func (d *dash) requireAdmin(w http.ResponseWriter, r *http.Request, scope string) (string, bool) {
	sub, ok := d.requireWriter(w, r)
	if !ok {
		return "", false
	}
	if !d.adminOn(r, scope) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return "", false
	}
	return strings.TrimPrefix(sub, "user:"), true
}
