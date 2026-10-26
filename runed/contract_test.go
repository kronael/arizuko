package runed

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	runedv1 "github.com/kronael/arizuko/runed/api/v1"
)

func newTestRuned(t *testing.T, rt Runtime) (*DB, *Server) {
	t.Helper()
	db, err := OpenMem()
	if err != nil {
		t.Fatalf("open mem db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	cfg, folders := renderWiring(t)
	mgr := NewManager(db, rt, ManagerConfig{
		Instance: "test",
		Cfg:      cfg, Folders: folders,
	})
	return db, NewServer(mgr, db, nil)
}

// TestContractRun is the spec 5/P-runed § Standalone-ready acceptance: accept a
// POST /v1/runs, broker a (faked) token, run the (Fake)Runtime with the
// brokered token + decoded RunRequest, and return {run_id, outcome:ok,
// session_id}. routd owns the MCP socket in-process now — runed is a pure
// spawner, so the contract is the spawn outcome, not federated callbacks.
func TestContractRun(t *testing.T) {
	// FakeRuntime stands in for the spawner: it sees the brokered token + the
	// fields decoded from RunRequest and returns the run outcome.
	var sawTurn Turn
	rt := FakeRuntime{Fn: func(_ context.Context, turn Turn) RunResult {
		sawTurn = turn
		return RunResult{Outcome: runedv1.OutcomeOK}
	}}

	db, srv := newTestRuned(t, rt)
	h := srv.Handler()

	rec := doRun(t, h, runedv1.RunRequest{Packages: []string{"core"}, Folder: "demo", ChatJID: "slack:T/C/U", TurnID: "wamid.X",
		MessageBatch: "rendered prompt", CallerSub: "user:u1",
		Model:           "claude-opus-4-8",
		ContainerConfig: map[string]any{"MaxChildren": 2}})
	if rec.Code != 200 {
		t.Fatalf("run status=%d body=%s", rec.Code, rec.Body.String())
	}
	var out runedv1.RunOutcome
	json.Unmarshal(rec.Body.Bytes(), &out)
	if out.Outcome != runedv1.OutcomeOK {
		t.Fatalf("outcome=%q want ok", out.Outcome)
	}
	if out.RunID == "" {
		t.Fatal("run_id empty")
	}

	// The substrate saw the request rendered into an envelope: the prompt and
	// the chat jid in the stdin frame, the model in the process environment,
	// the handle the claim pinned, and a mount table.
	frame := turnFrame(t, sawTurn)
	prompt, _ := frame["prompt"].(string)
	if !strings.HasSuffix(prompt, "\n\nrendered prompt") || frame["chatJid"] != "slack:T/C/U" {
		t.Fatalf("stdin frame not rendered from RunRequest: %+v", frame)
	}
	if sawTurn.Env["ARIZUKO_MODEL"] != "claude-opus-4-8" {
		t.Fatalf("Env[ARIZUKO_MODEL]=%q want claude-opus-4-8 (dropped)", sawTurn.Env["ARIZUKO_MODEL"])
	}
	if sawTurn.Handle == "" || len(sawTurn.Mounts) == 0 {
		t.Fatalf("turn has no handle or no mounts: %+v", sawTurn)
	}

	// the spawn was persisted; one session_log row.
	sp, err := db.SpawnByRunID(t.Context(), out.RunID)
	if err != nil {
		t.Fatalf("spawn not persisted: %v", err)
	}
	if sp.State != "exited" || sp.Outcome != runedv1.OutcomeOK {
		t.Fatalf("spawn state=%q outcome=%q", sp.State, sp.Outcome)
	}
	sessions, _ := db.RecentSessions(t.Context(), "demo", 10)
	if len(sessions) != 1 || sessions[0].Result != runedv1.OutcomeOK {
		t.Fatalf("session_log=%+v", sessions)
	}
}

// A second POST for a live folder returns busy without feeding its guest.
func TestLiveFolderRejectsFreshTurn(t *testing.T) {
	// FakeRuntime that blocks until we release it, simulating a live run.
	release := make(chan struct{})
	started := make(chan struct{})
	rt := FakeRuntime{Fn: func(ctx context.Context, turn Turn) RunResult {
		close(started)
		<-release
		return RunResult{Outcome: runedv1.OutcomeOK}
	}}
	db, srv := newTestRuned(t, rt)
	_ = db
	h := srv.Handler()

	// register a steerable live run before the second call.
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		done <- doRun(t, h, runedv1.RunRequest{Packages: []string{"core"}, Folder: "demo", ChatJID: "j", TurnID: "t1", MessageBatch: "first"})
	}()
	<-started
	// wire the steer callback as the production Runtime would.
	live, err := srv.mgr.ActiveRunID(t.Context(), "demo")
	if err != nil {
		t.Fatalf("ActiveRunID: %v", err)
	}
	steered := false
	srv.mgr.SetSteer(live, func(batch string) bool { steered = true; return true })

	rec := doRun(t, h, runedv1.RunRequest{Packages: []string{"core"}, Folder: "demo", ChatJID: "j", TurnID: "t2", MessageBatch: "steered"})
	var out runedv1.RunOutcome
	json.Unmarshal(rec.Body.Bytes(), &out)
	close(release)
	<-done
	if rec.Code != 200 || !out.Busy || out.Steered || out.RunID != "" || steered {
		t.Fatalf("fresh turn entered live run: status=%d out=%+v steered=%v", rec.Code, out, steered)
	}
}

func doRun(t *testing.T, h http.Handler, req runedv1.RunRequest) *httptest.ResponseRecorder {
	t.Helper()
	raw, _ := json.Marshal(req)
	r := httptest.NewRequest("POST", "/v1/runs", strings.NewReader(string(raw)))
	r.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}
