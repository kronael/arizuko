package runed

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/kronael/arizuko/auth"
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
	mux.HandleFunc("POST /v1/runs/stop", s.handleRunStop)
	mux.HandleFunc("GET /v1/runs/{run_id}", s.handleRunStatus)
	mux.HandleFunc("DELETE /v1/runs/{run_id}", s.handleRunKill)
	mux.HandleFunc("GET /v1/sessions", s.handleSessions)
	mux.HandleFunc("GET /v1/sessions/recent", s.handleRecentSessions)
	return mux
}

// authz verifies the bearer token and (when a scope is required) checks the
// token carries it. Returns the token's arz/folder claim + ok. verify==nil
// is open (single-tenant / local-dev): ok=true, folder="" (no folder bound).
func (s *Server) authz(w http.ResponseWriter, r *http.Request, needScope string) (folder string, ok bool) {
	if s.verify == nil {
		return "", true
	}
	_, scope, folder, err := s.verify.Verify(r)
	if err != nil {
		writeErr(w, 401, "unauthorized", err.Error())
		return "", false
	}
	if needScope != "" {
		res, verb, _ := strings.Cut(needScope, ":")
		if !auth.HasScope(scope, res, verb) {
			writeErr(w, 403, "forbidden", "missing scope "+needScope)
			return "", false
		}
	}
	return folder, true
}

// handleRun is the routd→runed contract (POST /v1/runs). Gated on the runs:run
// scope: routd's service token carries it, and an operator may be granted it
// for manual runs. runed is an internal docker-network service, but a scope
// check (not a bare bearer) is the gate so a stray agent/user token that
// reaches it cannot start arbitrary runs (spec 5/P § Auth).
func (s *Server) handleRun(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.authz(w, r, "runs:run"); !ok {
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

// handleRunStop is the operator-kill path (routd's /stop): map a folder to its
// live spawn and kill it. Gated on runs:kill (same scope as DELETE-by-run_id).
// killed=false is the no-active-container case; routd renders gated's text.
func (s *Server) handleRunStop(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.authz(w, r, "runs:kill"); !ok {
		return
	}
	var req runedv1.StopRunRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, "bad_request", err.Error())
		return
	}
	if req.Folder == "" {
		writeErr(w, 400, "missing_field", "folder required")
		return
	}
	runID, killed, err := s.mgr.StopFolder(req.Folder)
	if err != nil {
		writeErr(w, 500, "kill_failed", err.Error())
		return
	}
	writeJSON(w, 200, runedv1.StopRunResponse{Killed: killed, RunID: runID})
}

func (s *Server) handleRunStatus(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.authz(w, r, ""); !ok {
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
	if _, ok := s.authz(w, r, "runs:kill"); !ok {
		return
	}
	runID := r.PathValue("run_id")
	if err := s.mgr.Kill(runID); err == ErrNotFound {
		writeErr(w, 404, "unknown_run", "no such run")
		return
	} else if err != nil {
		writeErr(w, 500, "kill_failed", err.Error())
		return
	}
	writeJSON(w, 200, map[string]bool{"killed": true})
}

func (s *Server) handleSessions(w http.ResponseWriter, r *http.Request) {
	folder, ok := s.authz(w, r, "sessions:read")
	if !ok {
		return
	}
	// Folder is bound by the token's arz/folder claim, not the ?folder=
	// query param — a token cannot read another folder's history. Open mode
	// (verify==nil, folder="") honors the query param for local-dev.
	if folder == "" {
		folder = r.URL.Query().Get("folder")
	}
	rows, err := s.db.RecentSessions(folder, 0)
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

// handleRecentSessions is the routd→runed federation read (spec 5/P): the n
// newest session_log rows for a folder, full-fielded (group_folder + error),
// backing routd's new_session continuity hint + inspect_session tool. routd
// used to open runed.db directly; it calls this instead. Gated on sessions:read
// like GET /v1/sessions. Folder is bound by the token's arz/folder claim when
// present; routd's service token (folder="") honors the ?folder= query param.
func (s *Server) handleRecentSessions(w http.ResponseWriter, r *http.Request) {
	folder, ok := s.authz(w, r, "sessions:read")
	if !ok {
		return
	}
	if folder == "" {
		folder = r.URL.Query().Get("folder")
	}
	n := 0
	if v := r.URL.Query().Get("n"); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil {
			n = parsed
		}
	}
	rows, err := s.db.RecentSessionRecords(folder, n)
	if err != nil {
		writeErr(w, 500, "store_error", err.Error())
		return
	}
	out := runedv1.RecentSessionsResponse{}
	for _, x := range rows {
		out.Sessions = append(out.Sessions, runedv1.RecentSessionRecord{
			ID: x.ID, GroupFolder: x.GroupFolder, SessionID: x.SessionID,
			StartedAt: x.StartedAt, EndedAt: x.EndedAt, Result: x.Result,
			Error: x.Error, MessageCount: x.MessageCount,
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
