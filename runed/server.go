package runed

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/groupfolder"
	runedv1 "github.com/kronael/arizuko/runed/api/v1"
	"github.com/kronael/arizuko/substrate"
)

// Verifier offline-verifies inbound bearer tokens against authd's keys. runed
// is a verifier, not a signer (spec 5/P-runed § Auth). nil = open (local-dev).
type Verifier interface {
	Verify(r *http.Request) (sub string, scope []string, folder string, err error)
}

// Server is runed's HTTP face: POST /v1/runs (the routd↔runed contract) plus
// run status/kill and spawn-session history. The history reads live under
// `spawn_sessions`, NOT `sessions`: a resource's name is its wire identity
// and `sessions` is authd's (BUGS F46).
type Server struct {
	mgr    *Manager
	db     *DB
	verify Verifier
	// dashKeys gates GET /dash/runed/ (spec 6/28): the dashboard trusts the
	// proxyd-verified end-user identity, not a bearer. nil: local dev, gate
	// open.
	dashKeys *auth.KeySet
	// sub is what the substrate delivers on this configuration, shown on the
	// dashboard so an operator reads a downgrade where they look (spec 6/25
	// §3). The zero value renders nothing.
	sub substrate.Effective
}

// NewServer wires runed's HTTP server.
func NewServer(mgr *Manager, db *DB, verify Verifier) *Server {
	return &Server{mgr: mgr, db: db, verify: verify}
}

// SetSubstrate hands the dashboard the value main computed with Substrate.
func (s *Server) SetSubstrate(sub substrate.Effective) { s.sub = sub }

// SetDashKeySet wires the operator gate for GET /dash/runed/; nil falls open.
func (s *Server) SetDashKeySet(ks *auth.KeySet) { s.dashKeys = ks }

// Handler builds the routed mux. GET /health is public; everything else is
// bearer-gated. /openapi.json is NOT here: the binary adds it in newRunedMux
// alongside the resource list it advertises.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, 200, map[string]any{"ok": true}) })
	mux.HandleFunc("POST /v1/runs", s.handleRun)
	mux.HandleFunc("POST /v1/holds", s.handleHold)
	mux.HandleFunc("POST /v1/runs/stop", s.handleRunStop)
	mux.HandleFunc("GET /v1/runs/{run_id}", s.handleRunStatus)
	mux.HandleFunc("DELETE /v1/runs/{run_id}", s.handleRunKill)
	mux.HandleFunc("GET /v1/spawn_sessions", s.handleSessions)
	mux.HandleFunc("GET /v1/spawn_sessions/recent", s.handleRecentSessions)
	s.mountAudit(mux)
	// The operator dashboard (spec 6/28).
	mux.HandleFunc("GET /dash/runed/", s.handleDash)
	mux.HandleFunc("POST /dash/runed/kill", s.handleDashKill)
	return mux
}

// authz verifies the bearer token and, when a scope is required, checks the
// token carries it. Every caller takes the sub so no handler can record a
// kill as anonymous. verify==nil is open (local-dev).
func (s *Server) authz(w http.ResponseWriter, r *http.Request, needScope string) (sub, folder string, ok bool) {
	if s.verify == nil {
		return "", "", true
	}
	sub, scope, folder, err := s.verify.Verify(r)
	if err != nil {
		writeErr(w, 401, "unauthorized", err.Error())
		return "", "", false
	}
	if needScope != "" {
		res, verb, _ := strings.Cut(needScope, ":")
		if !auth.HasScope(scope, res, verb) {
			writeErr(w, 403, "forbidden", "missing scope "+needScope)
			return "", "", false
		}
	}
	return sub, folder, true
}

// ownsFolder reports whether a token scoped to tokenFolder may act on a run in
// target's folder. Empty tokenFolder (root, service:routd) owns everything; a
// folder-scoped token is confined to its own subtree. Mirrors routd.ownsFolder.
func ownsFolder(tokenFolder, target string) bool {
	if tokenFolder == "" || target == "" {
		return true
	}
	return groupfolder.Contains(tokenFolder, target)
}

// handleRun is the routd→runed contract (POST /v1/runs), gated on runs:run: a
// scope check, not a bare bearer, so a stray agent/user token that reaches
// this internal service cannot start arbitrary runs (spec 5/P-runed § Auth).
func (s *Server) handleRun(w http.ResponseWriter, r *http.Request) {
	_, folder, ok := s.authz(w, r, "runs:run")
	if !ok {
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
	// Bind the spawn to the caller's folder: a folder-scoped runs:run holder
	// must not spawn in another tenant's folder, which would run the agent
	// with that folder's secrets + network.
	if !ownsFolder(folder, string(req.Folder)) {
		writeErr(w, 403, "forbidden", "folder outside caller subtree: "+string(req.Folder))
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

// handleHold claims a folder's run slot for an external, folder-exclusive job
// (spec 5/8). A hold IS a run, so it takes the same runs:run scope and folder
// containment; releasing goes through DELETE /v1/runs/{run_id}.
func (s *Server) handleHold(w http.ResponseWriter, r *http.Request) {
	sub, folder, ok := s.authz(w, r, "runs:run")
	if !ok {
		return
	}
	var req runedv1.HoldRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, "bad_request", err.Error())
		return
	}
	if req.Folder == "" {
		writeErr(w, 400, "missing_field", "folder required")
		return
	}
	if !ownsFolder(folder, string(req.Folder)) {
		writeErr(w, 403, "forbidden", "folder outside caller subtree: "+string(req.Folder))
		return
	}
	out, err := s.mgr.Hold(r.Context(), req.Folder, req.Reason)
	s.auditRunSlot(r.Context(), sub, actionRunHold, string(req.Folder), out.RunID,
		holdParams(req.Reason, out.Busy), err)
	if err != nil {
		writeErr(w, 503, "hold_failed", err.Error())
		return
	}
	writeJSON(w, 200, out)
}

// handleRunStop is the operator-kill path (routd's /stop): map a folder to its
// live spawn and kill it. killed=false is the no-active-container case.
func (s *Server) handleRunStop(w http.ResponseWriter, r *http.Request) {
	sub, folder, ok := s.authz(w, r, "runs:kill")
	if !ok {
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
	if !ownsFolder(folder, req.Folder) {
		writeErr(w, 404, "unknown_run", "no such run") // uniform with status/kill; don't leak other folders
		return
	}
	runID, killed, err := s.mgr.StopFolder(r.Context(), req.Folder)
	s.auditRunSlot(r.Context(), sub, actionRunKill, req.Folder, runID, killParams(killed), err)
	if err != nil {
		writeErr(w, 500, "kill_failed", err.Error())
		return
	}
	writeJSON(w, 200, runedv1.StopRunResponse{Killed: killed, RunID: runID})
}

func (s *Server) handleRunStatus(w http.ResponseWriter, r *http.Request) {
	_, folder, ok := s.authz(w, r, "")
	if !ok {
		return
	}
	sp, err := s.db.SpawnByRunID(r.Context(), r.PathValue("run_id"))
	if err == ErrNotFound {
		writeErr(w, 404, "unknown_run", "no such run")
		return
	}
	if err != nil {
		writeErr(w, 500, "store_error", err.Error())
		return
	}
	if !ownsFolder(folder, sp.Folder) {
		writeErr(w, 404, "unknown_run", "no such run") // don't leak other folders' runs
		return
	}
	writeJSON(w, 200, runedv1.RunStatus{
		RunID: sp.RunID, Folder: sp.Folder, Topic: sp.Topic, State: sp.State,
		Outcome: sp.Outcome, SessionID: sp.SessionID, Steered: sp.Steered,
		CreatedAt: sp.CreatedAt, StartedAt: sp.StartedAt, EndedAt: sp.EndedAt,
	})
}

func (s *Server) handleRunKill(w http.ResponseWriter, r *http.Request) {
	sub, folder, ok := s.authz(w, r, "runs:kill")
	if !ok {
		return
	}
	runID := r.PathValue("run_id")
	// Resolve the run first, unconditionally: a folder-scoped token may only
	// kill its own subtree, and the audit row records WHICH folder's slot
	// this freed, root and service:routd included.
	sp, err := s.db.SpawnByRunID(r.Context(), runID)
	if err == ErrNotFound {
		writeErr(w, 404, "unknown_run", "no such run")
		return
	}
	if err != nil {
		writeErr(w, 500, "store_error", err.Error())
		return
	}
	if !ownsFolder(folder, sp.Folder) {
		writeErr(w, 404, "unknown_run", "no such run") // don't leak other folders' runs
		return
	}
	// An already-terminal run is a no-op 200 that leaves the spawns row
	// untouched, so killed=false is a fact only the audit row carries.
	killed := sp.State == "running" || sp.State == "queued"
	err = s.mgr.Kill(r.Context(), runID)
	s.auditRunSlot(r.Context(), sub, actionRunKill, sp.Folder, runID, killParams(killed && err == nil), err)
	if err == ErrNotFound {
		writeErr(w, 404, "unknown_run", "no such run")
		return
	}
	if err != nil {
		writeErr(w, 500, "kill_failed", err.Error())
		return
	}
	writeJSON(w, 200, map[string]bool{"killed": true})
}

func (s *Server) handleSessions(w http.ResponseWriter, r *http.Request) {
	_, folder, ok := s.authz(w, r, "sessions:read")
	if !ok {
		return
	}
	// Folder is bound by the token's arz/folder claim, not ?folder=: a token
	// cannot read another folder's history. Open mode honors the query param.
	if folder == "" {
		folder = r.URL.Query().Get("folder")
	}
	rows, err := s.db.RecentSessions(r.Context(), folder, 0)
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

// handleRecentSessions is the routd→runed federation read (spec 5/P-runed): the n
// newest session_log rows for a folder, since routd never opens runed's store.
// Folder is bound by the token's arz/folder claim when present; routd's
// service token honors ?folder=.
func (s *Server) handleRecentSessions(w http.ResponseWriter, r *http.Request) {
	_, folder, ok := s.authz(w, r, "sessions:read")
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
	rows, err := s.db.RecentSessions(r.Context(), folder, n)
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
