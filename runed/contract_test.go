package runed

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	routdv1 "github.com/kronael/arizuko/routd/api/v1"
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
// POST /v1/runs, broker a (faked) token, run a FakeRuntime that connects
// back (here: forwards a reply to a stub routd via the Federator) and
// submits a turn, and return {run_id, outcome:ok, session_id}.
func TestContractRun(t *testing.T) {
	// stub routd: records the callback bodies.
	var replies, results int32
	stubRoutd := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/reply"):
			atomic.AddInt32(&replies, 1)
			json.NewEncoder(w).Encode(routdv1.SendResult{MessageID: "out1", PlatformID: "pid1", Status: "sent"})
		case strings.HasSuffix(r.URL.Path, "/result"):
			atomic.AddInt32(&results, 1)
			json.NewEncoder(w).Encode(routdv1.TurnResultAck{Recorded: true})
		default:
			w.WriteHeader(404)
		}
	}))
	defer stubRoutd.Close()
	fed := NewFederator(stubRoutd.URL)

	// FakeRuntime drives the federated callback + submit_turn, the way a
	// real agent's tools would.
	rt := FakeRuntime{Fn: func(ctx context.Context, spec RunSpec) RunResult {
		if spec.Token != "fake.jws" {
			t.Errorf("agent token=%q want brokered fake.jws", spec.Token)
		}
		_, _ = fed.Forward(ctx, "reply", spec.TurnID, spec.Token, "i1",
			map[string]any{"jid": spec.ChatJID, "text": "ack"})
		_, _ = fed.Result(ctx, spec.TurnID, spec.Token, "r1", routdv1.TurnResult{
			TurnID: spec.TurnID, SessionID: "sess-runed", Status: "success"})
		return RunResult{Outcome: runedv1.OutcomeOK, NewSessionID: "sess-runed"}
	}}

	db, srv := newTestRuned(t, rt)
	h := srv.Handler()

	rec := doRun(t, h, runedv1.RunRequest{
		Folder: "demo", ChatJID: "slack:T/C/U", TurnID: "wamid.X",
		MessageBatch: "rendered prompt", CallerSub: "user:u1",
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
	if atomic.LoadInt32(&replies) != 1 || atomic.LoadInt32(&results) != 1 {
		t.Fatalf("callbacks replies=%d results=%d want 1,1", replies, results)
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
