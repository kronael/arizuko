package routd

import (
	"encoding/json"
	"net/http"
	"strconv"
)

// tasks_http.go is the cockpit-dashboard task surface (spec 6/8): list/detail/
// run-feed reads (tasks:read) + patch/delete writes (tasks:write). The
// fire-loop endpoints (due/runlog/reschedule) stay in server.go — those serve
// timed, these serve the operator dashboard.

// handleTaskList serves GET /v1/tasks?folder=&status= (tasks:read).
// folder="" means all tasks across every group (root scope).
func (s *Server) handleTaskList(w http.ResponseWriter, r *http.Request) {
	if !s.authed(w, r, "tasks:read") {
		return
	}
	folder := r.URL.Query().Get("folder")
	statusFilter := r.URL.Query().Get("status")
	tasks := s.db.Tasks(folder, folder == "")
	if statusFilter != "" {
		kept := tasks[:0]
		for _, t := range tasks {
			if t.Status == statusFilter {
				kept = append(kept, t)
			}
		}
		tasks = kept
	}
	writeJSON(w, 200, map[string]any{"tasks": tasks})
}

// handleTaskGet serves GET /v1/tasks/{id} (tasks:read).
func (s *Server) handleTaskGet(w http.ResponseWriter, r *http.Request) {
	if !s.authed(w, r, "tasks:read") {
		return
	}
	t, ok := s.db.GetTask(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "not_found", "task not found")
		return
	}
	writeJSON(w, 200, t)
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

// handleTaskPatch serves PATCH /v1/tasks/{id} (tasks:write).
// Accepts JSON body: {"status": "paused|active"} for pause/resume, or
// {"next_run": "<RFC3339>"} for run-now, or both.
func (s *Server) handleTaskPatch(w http.ResponseWriter, r *http.Request) {
	if !s.authed(w, r, "tasks:write") {
		return
	}
	var body struct {
		Status  string `json:"status"`
		NextRun string `json:"next_run"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 400, "bad_request", err.Error())
		return
	}
	if body.Status == "" && body.NextRun == "" {
		writeErr(w, 400, "bad_request", "status or next_run required")
		return
	}
	id := r.PathValue("id")
	if err := s.db.PatchTask(id, body.Status, body.NextRun); err != nil {
		writeErr(w, 500, "internal", err.Error())
		return
	}
	t, ok := s.db.GetTask(id)
	if !ok {
		writeErr(w, 404, "not_found", "task not found")
		return
	}
	writeJSON(w, 200, t)
}

// handleTaskDelete serves DELETE /v1/tasks/{id} (tasks:write). Irreversible.
func (s *Server) handleTaskDelete(w http.ResponseWriter, r *http.Request) {
	if !s.authed(w, r, "tasks:write") {
		return
	}
	if err := s.db.DeleteTask(r.PathValue("id")); err != nil {
		writeErr(w, 500, "internal", err.Error())
		return
	}
	w.WriteHeader(204)
}
