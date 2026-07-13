package routd

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/kronael/arizuko/resreg"
)

// tasks_http.go is the /v1/tasks operator REST face. Its list/get/patch/delete
// verbs ride the SAME shared handler (scheduledTasksHandler,
// scheduled_tasks_resource.go) the agent's schedule/pause/resume/cancel/list MCP
// tools use — the spec 5/16 REST-face fold. resreg.RegisterREST mounts them on a
// copy of s.scheduledTasksResource() whose Endpoints are overridden with the
// /v1/tasks CRUD verbs and a REST-specific Caller + Gate injected, so ALL the
// REST-only policy (scope + JWT-folder containment) lives here and the shared
// handler stays surface-agnostic. The two run-log READERS stay hand-rolled: they
// are the operator dashboard's run-feed, not resource CRUD; the fire-loop
// control-plane endpoints (due/runlog/reschedule) live in server.go.

// mountTasks wires the /v1/tasks CRUD REST surface. list/get/patch/cancel share
// scheduledTasksHandler through resreg with the operator/human REST Gate +
// Caller injected; the id path placeholder is named {taskId} so it merges into
// Args under the same key the shared handler reads for the MCP tools.
func (s *Server) mountTasks(mux *http.ServeMux) {
	// Operator/human REST face: per-task containment is ownsFolder on the caller's
	// JWT folder (own-or-descendant), NOT the agent tier model — this is what closes
	// the live cross-tenant leak (a tier-0 tenant / same-world tier-1 folder could
	// delete another folder's task) and lets an operator manage its whole subtree
	// (which the exact-match tier-2 cap wrongly denied). tasksRESTGate keeps the
	// coarse scope + JWT-folder guard; contain does the per-target decision.
	contain := func(c resreg.Caller, _ resreg.Action, target string) error {
		if ownsFolder(c.Folder, target) {
			return nil
		}
		return resreg.Errorf(http.StatusForbidden, "task owner outside caller subtree: %s", target)
	}
	res := s.scheduledTasksResource(contain)
	res.Endpoints = []resreg.Endpoint{
		{Verb: "GET", Path: "/v1/tasks", Action: resreg.ActionList},
		{Verb: "GET", Path: "/v1/tasks/{taskId}", Action: resreg.ActionGet},
		{Verb: "PATCH", Path: "/v1/tasks/{taskId}", Action: tasksActionPatch},
		{Verb: "DELETE", Path: "/v1/tasks/{taskId}", Action: tasksActionCancel},
	}
	res.Gate = s.tasksRESTGate
	resreg.RegisterREST(mux, res, s.tasksRESTCaller)
}

// tasksRESTCaller builds the REST Caller for the shared scheduled_tasks handler.
// It resolves identity via the Verifier, then sets Caller.Folder to the folder
// the handler keys on (tasksTarget). The held scopes + JWT folder ride in Claims
// for tasksRESTGate. A nil Verifier is open (single-tenant/local-dev, mirroring
// s.authz): empty principal + empty JWT folder (so list spans all).
func (s *Server) tasksRESTCaller(r *http.Request) (resreg.Caller, error) {
	var sub, jwtFolder string
	var scope []string
	if s.verify != nil {
		var err error
		sub, scope, jwtFolder, err = s.verify.Verify(r)
		if err != nil {
			return resreg.Caller{}, err
		}
	}
	return resreg.Caller{
		Sub:    sub,
		Folder: tasksTarget(r, jwtFolder),
		Claims: map[string]string{
			"jwt_folder": jwtFolder,
			"scopes":     strings.Join(scope, " "),
		},
	}, nil
}

// tasksTarget resolves the folder the shared handler keys on. A per-task op
// (GET-one/PATCH/DELETE, {taskId} present) uses the caller's own JWT folder — the
// real per-task containment is the handler's AuthorizeStructural on the resolved
// task owner, not this folder. GET list reads ?folder= (empty → "" ONLY for a
// root/service token with no folder claim, so the list spans every folder; else
// the caller's own JWT folder, so a folder-scoped token — even a tier-0 top-level
// one — lists only its own tasks, the 5/16 list-all leak guard).
func tasksTarget(r *http.Request, jwtFolder string) string {
	if r.PathValue("taskId") != "" {
		return jwtFolder // per-task op: containment is per-task in the handler
	}
	if q := r.URL.Query().Get("folder"); q != "" {
		return q
	}
	return jwtFolder // "" for a root/service token (list-all), else own folder
}

// tasksRESTGate is the operator/human REST twin of the agent socket's tier Gate:
// an any-of scope check on the caller's held scopes, then JWT-folder containment
// over the target (Caller.Folder). Same shared handler, a different injected gate
// (CLAUDE.md "auth is a uniform middleware"): tasks:read for GET, tasks:write for
// mutations, then ownsFolder. For a per-task op Caller.Folder is the JWT folder,
// so ownsFolder is a no-op there and the handler's AuthorizeStructural does the
// real per-task cap. A nil Verifier is open (mirrors s.authz).
func (s *Server) tasksRESTGate(x resreg.Execution, _ string, _ map[string]string) error {
	if s.verify == nil {
		return nil
	}
	held := strings.Fields(x.Caller.Claims["scopes"])
	want := []string{"tasks:read", "tasks:read:own_group"}
	if x.Action.Mutates() {
		want = []string{"tasks:write", "tasks:write:own_group"}
	}
	if !hasAnyScope(held, want) {
		return resreg.Errorf(http.StatusForbidden, "missing scope %s", strings.Join(want, " or "))
	}
	if !ownsFolder(x.Caller.Claims["jwt_folder"], x.Caller.Folder) {
		return resreg.Errorf(http.StatusForbidden, "folder outside caller subtree: %s", x.Caller.Folder)
	}
	return nil
}

// handleTaskRunLogs serves GET /v1/tasks/{id}/runs?limit= (tasks:read).
func (s *Server) handleTaskRunLogs(w http.ResponseWriter, r *http.Request) {
	if !s.authed(w, r, "tasks:read") {
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	logs := s.db.TaskRunLogs(r.PathValue("id"), limit)
	writeJSON(w, 200, map[string]any{"runs": logs})
}

// handleAllRunLogs serves GET /v1/tasks/runs?limit= (tasks:read).
// Cross-task recent run feed for the timed dashboard /runs page.
func (s *Server) handleAllRunLogs(w http.ResponseWriter, r *http.Request) {
	if !s.authed(w, r, "tasks:read") {
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	logs := s.db.AllRunLogs(limit)
	writeJSON(w, 200, map[string]any{"runs": logs})
}
