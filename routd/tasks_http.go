package routd

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/kronael/arizuko/resreg"
)

// Task REST operations share scheduledTasksHandler with MCP and inject
// REST-specific identity, scope, and folder containment. Run-log readers remain
// hand-rolled dashboard endpoints.

// mountTasks mounts resources.ScheduledTasksEndpoints verbatim — the SAME slice
// deriveMCPTools and /openapi.json read, so the served routes, the agent tools
// and the doc cannot drift. NEVER reintroduce an inline Endpoints literal here;
// endpoints_source_test.go probes this mux's patterns to catch exactly that.
func (s *Server) mountTasks(mux *http.ServeMux) {
	// The gate checks coarse scope and folder; this checks the resolved task owner.
	contain := func(c resreg.Caller, _ resreg.Action, target string) error {
		if ownsFolder(c.Folder, target) {
			return nil
		}
		return resreg.Errorf(http.StatusForbidden, "task owner outside caller subtree: %s", target)
	}
	// REST has no /root elevation; list-all rides the jwt_folder=="" claim (the
	// handler's own branch), so pass elevated=false.
	res := s.scheduledTasksResource(contain, false)
	res.Gate = s.tasksRESTGate
	resreg.RegisterREST(mux, res, s.tasksRESTCaller)
}

// tasksRESTCaller sets the task target as Caller.Folder and passes held scopes
// and the JWT folder to the REST gate. A nil Verifier is open.
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

// tasksTarget uses the JWT folder for per-task operations; the handler checks
// the resolved task owner. Lists accept an explicit folder, otherwise a scoped
// caller sees its own folder and an empty root/service folder sees all.
func tasksTarget(r *http.Request, jwtFolder string) string {
	if r.PathValue("taskId") != "" {
		return jwtFolder // per-task op: containment is per-task in the handler
	}
	if q := r.URL.Query().Get("folder"); q != "" {
		return q
	}
	return jwtFolder // "" for a root/service token (list-all), else own folder
}

// tasksRESTGate applies REST scopes and JWT-folder containment. Per-task owner
// containment remains in the shared handler.
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
