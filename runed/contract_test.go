package runed

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	runedv1 "github.com/kronael/arizuko/runed/api/v1"
	"github.com/kronael/arizuko/types"
)

func newTestRuned(t *testing.T, rt Runtime) (*DB, *Server) {
	t.Helper()
	db, err := OpenMem()
	if err != nil {
		t.Fatalf("open mem db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	broker := NewStaticBroker("fake.jws", "jti-fixed")
	mgr := NewManager(db, rt, broker, ManagerConfig{
		Scopes:   []types.Scope{"messages:send:own_group", "chats:read:own_group"},
		Instance: "test",
	})
	return db, NewServer(mgr, db, nil)
}

// TestContractRun is the spec 5/P § Standalone-ready acceptance: accept a
// POST /v1/runs, broker a (faked) token, run the (Fake)Runtime with the
// brokered token + decoded RunRequest, and return {run_id, outcome:ok,
// session_id}. routd owns the MCP socket in-process now — runed is a pure
// spawner, so the contract is the spawn outcome, not federated callbacks.
func TestContractRun(t *testing.T) {
	// FakeRuntime stands in for the spawner: it sees the brokered token + the
	// fields decoded from RunRequest and returns the run outcome.
	var sawSpec RunSpec
	rt := FakeRuntime{Fn: func(_ context.Context, spec RunSpec) RunResult {
		sawSpec = spec
		return RunResult{Outcome: runedv1.OutcomeOK, NewSessionID: "sess-runed"}
	}}

	db, srv := newTestRuned(t, rt)
	h := srv.Handler()

	rec := doRun(t, h, runedv1.RunRequest{
		Folder: "demo", ChatJID: "slack:T/C/U", TurnID: "wamid.X",
		MessageBatch: "rendered prompt", CallerSub: "user:u1",
		Model:            "claude-opus-4-8",
		ContainerConfig:  map[string]any{"MaxChildren": 2},
		CapabilityScopes: []types.Scope{"messages:send:own_group"},
	})
	if rec.Code != 200 {
		t.Fatalf("run status=%d body=%s", rec.Code, rec.Body.String())
	}
	var out runedv1.RunOutcome
	json.Unmarshal(rec.Body.Bytes(), &out)
	if out.Outcome != runedv1.OutcomeOK {
		t.Fatalf("outcome=%q want ok", out.Outcome)
	}
	if out.SessionID != "sess-runed" {
		t.Fatalf("session_id=%q want sess-runed", out.SessionID)
	}
	if out.RunID == "" {
		t.Fatal("run_id empty")
	}

	// the Runtime saw the brokered token + the fields decoded from RunRequest
	// (Model + ContainerConfig are forwarded, not dropped).
	if sawSpec.Token != "fake.jws" {
		t.Fatalf("agent token=%q want brokered fake.jws", sawSpec.Token)
	}
	if sawSpec.MessageBatch != "rendered prompt" || sawSpec.ChatJID != "slack:T/C/U" {
		t.Fatalf("spec not decoded from RunRequest: %+v", sawSpec)
	}
	if sawSpec.Model != "claude-opus-4-8" {
		t.Fatalf("spec.Model=%q want claude-opus-4-8 (dropped)", sawSpec.Model)
	}
	if v, _ := sawSpec.ContainerConfig["MaxChildren"]; v != float64(2) {
		t.Fatalf("spec.ContainerConfig=%+v want MaxChildren=2 (dropped)", sawSpec.ContainerConfig)
	}

	// the spawn + brokered token were persisted; one session_log row.
	sp, err := db.GetSpawn(out.RunID)
	if err != nil {
		t.Fatalf("spawn not persisted: %v", err)
	}
	if sp.MCPTokenJTI != "jti-fixed" {
		t.Fatalf("brokered token jti=%q want jti-fixed", sp.MCPTokenJTI)
	}
	if sp.State != "exited" || sp.Outcome != runedv1.OutcomeOK {
		t.Fatalf("spawn state=%q outcome=%q", sp.State, sp.Outcome)
	}
	sessions, _ := db.RecentSessions("demo", 10)
	if len(sessions) != 1 || sessions[0].Result != runedv1.OutcomeOK {
		t.Fatalf("session_log=%+v", sessions)
	}
}

// TestSteerIntoRunning checks a second POST /v1/runs for a folder with a
// live spawn returns steered:true (an ack), not a fresh turn-boundary.
func TestSteerIntoRunning(t *testing.T) {
	// FakeRuntime that blocks until we release it, simulating a live run.
	release := make(chan struct{})
	started := make(chan struct{})
	rt := FakeRuntime{Fn: func(ctx context.Context, spec RunSpec) RunResult {
		close(started)
		<-release
		return RunResult{Outcome: runedv1.OutcomeOK, NewSessionID: "s1"}
	}}
	db, srv := newTestRuned(t, rt)
	_ = db
	h := srv.Handler()

	// register a steerable live run before the second call.
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		done <- doRun(t, h, runedv1.RunRequest{Folder: "demo", ChatJID: "j", TurnID: "t1", MessageBatch: "first"})
	}()
	<-started
	// wire the steer callback as the production Runtime would.
	srv.mgr.SetSteer("demo", srv.mgr.ActiveRunID("demo"), func(batch string) bool { return true })

	rec := doRun(t, h, runedv1.RunRequest{Folder: "demo", ChatJID: "j", TurnID: "t2", MessageBatch: "steered"})
	var out runedv1.RunOutcome
	json.Unmarshal(rec.Body.Bytes(), &out)
	if !out.Steered {
		t.Fatalf("second run steered=%v want true (out=%+v)", out.Steered, out)
	}
	close(release)
	<-done
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
