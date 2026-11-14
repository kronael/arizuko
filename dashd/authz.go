package main

import (
	"context"
	"log/slog"
	"net/http"
	"strings"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/groupfolder"
	"github.com/kronael/arizuko/router"
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

func (d *dash) jidFolder(jid string) string {

	if d.routdStore() == nil {
		return ""
	}
	if strings.HasPrefix(jid, "web:") || strings.HasPrefix(jid, "hook:") {
		target, err := store.RouteTokenTargetByJID(
			context.Background(), d.routd, jid)
		if err != nil {

			slog.Error("route token target read failed; message stays invisible",
				"jid", jid, "err", err)
			return ""
		}
		if target != "" {
			return target
		}
		return groupfolder.JidFolder(jid)
	}
	routes, err := d.routeSnapshot.Read(context.Background(), d.routd, store.OwnerScope(store.OwnerRoutd), func(context.Context) ([]core.Route, error) {
		return d.routdStore().RouteTable()
	})
	if err != nil {
		slog.Error("read dashboard routes", "err", err)
		return ""
	}
	return core.ParseRouteTarget(router.ResolveRoute(core.Message{ChatJID: jid, Verb: "message"}, routes)).Folder
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

func (d *dash) countVisibleGroups(p auth.Policy) int {
	st := d.ds()
	return d.countVisible("groups", p, st.GroupFolders, folderIdentity)
}

func (d *dash) countVisibleErroredChats(p auth.Policy) int {
	st := d.ds()
	return d.countVisible("errored-chats", p, st.ErroredChatJIDs, d.jidFolder)
}

func (d *dash) countVisibleFailedTasks(p auth.Policy) int {
	st := d.ds()
	return d.countVisible("failed-tasks", p, st.FailedTaskOwners, folderIdentity)
}

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
