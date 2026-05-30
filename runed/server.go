package runed

import (
	"context"
	"encoding/json"
	"net/http"

	runedv1 "github.com/kronael/arizuko/runed/api/v1"
)

// Verifier offline-verifies inbound bearer tokens (routd service / operator
// / agent) against authd's keys. runed is a verifier, not a signer (spec
// 5/P § Auth). nil = open (single-tenant / local-dev).
type Verifier interface {
	Verify(r *http.Request) (sub string, scope []string, folder string, err error)
}

// Server is runed's HTTP face: POST /v1/runs (the routd↔runed contract) +
// run status/kill + session history. The Manager owns the run lifecycle.
type Server struct {
	mgr    *Manager
	db     *DB
	verify Verifier
}

// NewServer wires runed's HTTP server.
func NewServer(mgr *Manager, db *DB, verify Verifier) *Server {
	return &Server{mgr: mgr, db: db, verify: verify}
}

// Handler builds the routed mux. GET /health and /openapi.json are public;
// everything else is bearer-gated.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, 200, map[string]any{"ok": true}) })
	mux.HandleFunc("POST /v1/runs", s.handleRun)
	mux.HandleFunc("GET /v1/runs/{run_id}", s.handleRunStatus)
	mux.HandleFunc("DELETE /v1/runs/{run_id}", s.handleRunKill)
	mux.HandleFunc("GET /v1/sessions", s.handleSessions)
	return mux
}

func (s *Server) authed(w http.ResponseWriter, r *http.Request) bool {
	if s.verify == nil {
		return true
	}
	if _, _, _, err := s.verify.Verify(r); err != nil {
		writeErr(w, 401, "unauthorized", err.Error())
		return false
	}
	return true
}

func (s *Server) handleRun(w http.ResponseWriter, r *http.Request) {
	if !s.authed(w, r) {
		return
	}
	var req runedv1.RunRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, "bad_request", err.Error())
		return
	}
	if req.Folder == "" {
		writeErr(w, 400, "missing_field", "folder required")
		return
	}
	out, err := s.mgr.Run(r.Context(), req)
	if err != nil {
		// transport-class error to the caller (queue shutting down etc.)
		writeErr(w, 503, "run_failed", err.Error())
		return
	}
	writeJSON(w, 200, out)
}

func (s *Server) handleRunStatus(w http.ResponseWriter, r *http.Request) {
	if !s.authed(w, r) {
		return
	}
	sp, err := s.db.GetSpawn(r.PathValue("run_id"))
	if err != nil {
		writeErr(w, 404, "unknown_run", "no such run")
		return
	}
	writeJSON(w, 200, runedv1.RunStatus{
		RunID: sp.RunID, Folder: sp.Folder, Topic: sp.Topic, State: sp.State,
		Outcome: sp.Outcome, SessionID: sp.SessionID, Steered: sp.Steered,
		CreatedAt: sp.CreatedAt, StartedAt: sp.StartedAt, EndedAt: sp.EndedAt,
	})
}

func (s *Server) handleRunKill(w http.ResponseWriter, r *http.Request) {
	if !s.authed(w, r) {
		return
	}
	runID := r.PathValue("run_id")
	sp, err := s.db.GetSpawn(runID)
	if err != nil {
		writeErr(w, 404, "unknown_run", "no such run")
		return
	}
	if sp.State == "running" || sp.State == "queued" {
		_ = s.db.EndSpawn(runID, "killed", "error", -1)
	}
	writeJSON(w, 200, map[string]bool{"killed": true})
}

func (s *Server) handleSessions(w http.ResponseWriter, r *http.Request) {
	if !s.authed(w, r) {
		return
	}
	rows, err := s.db.RecentSessions(r.URL.Query().Get("folder"), 0)
	if err != nil {
		writeErr(w, 500, "store_error", err.Error())
		return
	}
	out := runedv1.SessionsResponse{}
	for _, x := range rows {
		out.Sessions = append(out.Sessions, runedv1.SessionRow{
			ID: x.ID, SessionID: x.SessionID, StartedAt: x.StartedAt,
			EndedAt: x.EndedAt, Result: x.Result, MessageCount: x.MessageCount,
		})
	}
	writeJSON(w, 200, out)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, runedv1.Err{Error: code, Message: msg})
}

var _ = context.Background
