package runed

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	runedv1 "github.com/kronael/arizuko/runed/api/v1"
	"github.com/kronael/arizuko/types"
)

// specRecorder is a Runtime that captures the LAST RunSpec it was handed and
// returns a scripted outcome — the integration seam for asserting routd's
// RunRequest threads end-to-end onto RunSpec through the real HTTP handler +
// Manager, no docker.
type specRecorder struct {
	last atomic.Pointer[RunSpec]
	res  RunResult
}

func (r *specRecorder) Run(_ context.Context, spec RunSpec) RunResult {
	cp := spec
	r.last.Store(&cp)
	return r.res
}
func (*specRecorder) Kill(string) error { return nil }

// TestHTTPRunThreadsFullRunSpec: POST /v1/runs over the real handler+Manager
// reaches the Runtime with EVERY routd-supplied field threaded onto RunSpec —
// folder, topic, channel, session, message, caller_sub, turn_id,
// trigger_sender, model, container_config, grants, egress allowlist. A dropped
// field silently runs the agent mis-configured (wrong output style, wrong
// egress, missing grants). The lone end-to-end field-fidelity gate over HTTP
// (contract_test asserts a SUBSET, no grants/egress/channel).
func TestHTTPRunThreadsFullRunSpec(t *testing.T) {
	rec := &specRecorder{res: RunResult{Outcome: runedv1.OutcomeOK, NewSessionID: "sess-x"}}
	// service:routd drives runs (folder=""), so any target folder is allowed.
	_, srv := serverWith(t, rec, fakeVerifier{scope: []string{"runs:run"}})
	h := srv.Handler()

	got := doRun(t, h, runedv1.RunRequest{
		Folder: "acme/eng", Topic: "incident", ChatJID: "telegram:42",
		Channel: "telegram", SessionID: "resume-me", MessageBatch: "rendered",
		TriggerSender: "user:trig", CallerSub: "user:caller", TurnID: "wamid.Z",
		CapabilityScopes: []types.Scope{"messages:send:own_group", "admin:everything"},
		Model:            "claude-opus-4-8",
		ContainerConfig:  map[string]any{"MaxChildren": 4},
		Grants:           []string{"!share_mount(acme)"},
		EgressAllowlist:  []string{"api.acme.com", "github.com"},
	})
	if got.Code != 200 {
		t.Fatalf("run = %d want 200 (body=%s)", got.Code, got.Body.String())
	}
	spec := rec.last.Load()
	if spec == nil {
		t.Fatal("runtime never saw a RunSpec")
	}
	if spec.Folder != "acme/eng" || spec.Topic != "incident" || spec.ChatJID != "telegram:42" ||
		spec.Channel != "telegram" || spec.SessionID != "resume-me" || spec.MessageBatch != "rendered" ||
		spec.TriggerSender != "user:trig" || spec.CallerSub != "user:caller" || spec.TurnID != "wamid.Z" ||
		spec.Model != "claude-opus-4-8" {
		t.Fatalf("scalar fields not threaded: %+v", spec)
	}
	if v := spec.ContainerConfig["MaxChildren"]; v != float64(4) {
		t.Fatalf("container_config not threaded: %+v", spec.ContainerConfig)
	}
	if len(spec.Grants) != 1 || spec.Grants[0] != "!share_mount(acme)" {
		t.Fatalf("grants not threaded: %+v", spec.Grants)
	}
	if len(spec.EgressAllowlist) != 2 || spec.EgressAllowlist[0] != "api.acme.com" {
		t.Fatalf("egress_allowlist not threaded: %+v", spec.EgressAllowlist)
	}
	// runTTL is threaded from the Manager config default, not the request.
	if spec.RunTTL != defaultRunTTL {
		t.Fatalf("RunTTL=%s want default %s", spec.RunTTL, defaultRunTTL)
	}
	// the brokered token reached the runtime (downscoped, never minted by runed).
	if spec.Token != "jws" {
		t.Fatalf("brokered token=%q want jws", spec.Token)
	}
}

// TestHTTPRunBrokersDownscopedScope: the scope handed to the broker for an HTTP
// run is the INTERSECTION of the manager ceiling and the request's
// CapabilityScopes — end to end. (manager_test proves it at the Manager seam;
// this proves the handler doesn't bypass the downscope.)
func TestHTTPRunBrokersDownscopedScope(t *testing.T) {
	cb := &capBroker{}
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	mgr := NewManager(db, &specRecorder{res: RunResult{Outcome: runedv1.OutcomeOK}}, cb, ManagerConfig{
		Scopes: []types.Scope{"messages:send:own_group"}, Instance: "test", MaxConcurrent: 5,
	})
	srv := NewServer(mgr, db, fakeVerifier{scope: []string{"runs:run"}})

	got := doRun(t, srv.Handler(), runedv1.RunRequest{
		Folder: "demo", MessageBatch: "m",
		CapabilityScopes: []types.Scope{"messages:send:own_group", "admin:everything"},
	})
	if got.Code != 200 {
		t.Fatalf("run = %d want 200", got.Code)
	}
	if len(cb.want) != 1 || cb.want[0] != "messages:send:own_group" {
		t.Fatalf("brokered scope over HTTP = %v want [messages:send:own_group] (escalation must be clamped)", cb.want)
	}
}

// TestHTTPRunMissingFolder: POST /v1/runs with no folder is 400 missing_field;
// the runtime is never reached.
func TestHTTPRunMissingFolder(t *testing.T) {
	rec := &specRecorder{res: RunResult{Outcome: runedv1.OutcomeOK}}
	_, srv := serverWith(t, rec, fakeVerifier{scope: []string{"runs:run"}})
	got := postJSON(t, srv.Handler(), "/v1/runs", `{"message_batch":"m"}`)
	if got.Code != 400 {
		t.Fatalf("missing folder = %d want 400 (body=%s)", got.Code, got.Body.String())
	}
	var e runedv1.Err
	json.Unmarshal(got.Body.Bytes(), &e)
	if e.Error != "missing_field" {
		t.Fatalf("error code=%q want missing_field", e.Error)
	}
	if rec.last.Load() != nil {
		t.Fatal("runtime reached on a 400-rejected request")
	}
}

// TestHTTPRunMalformedBody: a non-JSON body is 400 bad_request.
func TestHTTPRunMalformedBody(t *testing.T) {
	_, srv := serverWith(t, &specRecorder{}, fakeVerifier{scope: []string{"runs:run"}})
	if got := postJSON(t, srv.Handler(), "/v1/runs", `{not json`); got.Code != 400 {
		t.Fatalf("malformed body = %d want 400", got.Code)
	}
}

// TestHTTPRunLifecycle: the full HTTP lifecycle for ONE run — POST /v1/runs
// while the runtime blocks (state=running), GET /v1/runs/{id} reports running,
// DELETE /v1/runs/{id} kills it (Runtime.Kill + state=killed). Real handlers
// via httptest, no docker.
func TestHTTPRunLifecycle(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	rec := &killRecorder{FakeRuntime: FakeRuntime{Fn: func(_ context.Context, _ RunSpec) RunResult {
		close(started)
		<-release
		return RunResult{Outcome: runedv1.OutcomeOK, NewSessionID: "s-final"}
	}}}
	db, srv := serverWith(t, rec, fakeVerifier{scope: []string{"runs:run", "runs:kill"}})
	h := srv.Handler()

	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- doRun(t, h, runedv1.RunRequest{Folder: "demo", TurnID: "t1", MessageBatch: "m"}) }()
	<-started
	runID := srv.mgr.ActiveRunID("demo")
	if runID == "" {
		t.Fatal("no active run after spawn started")
	}

	st := req(t, h, "GET", "/v1/runs/"+runID)
	if st.Code != 200 {
		t.Fatalf("status = %d want 200 (body=%s)", st.Code, st.Body.String())
	}
	var status runedv1.RunStatus
	json.Unmarshal(st.Body.Bytes(), &status)
	if status.State != "running" || status.Folder != "demo" {
		t.Fatalf("status=%+v want state=running folder=demo", status)
	}

	del := req(t, h, "DELETE", "/v1/runs/"+runID)
	if del.Code != 200 {
		t.Fatalf("kill = %d want 200", del.Code)
	}
	if atomic.LoadInt32(&rec.killed) != 1 {
		t.Fatalf("Runtime.Kill called %d times want 1", rec.killed)
	}
	if sp, _ := db.GetSpawn(runID); sp.State != "killed" {
		t.Fatalf("post-kill state=%q want killed", sp.State)
	}
	close(release)
	<-done
}

// TestHTTPRunStopLifecycle: POST /v1/runs then POST /v1/runs/stop {folder}
// resolves the live spawn and kills it (the routd /stop path), and a second
// stop on the now-idle folder is killed:false.
func TestHTTPRunStopLifecycle(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	rec := &killRecorder{FakeRuntime: FakeRuntime{Fn: func(_ context.Context, _ RunSpec) RunResult {
		close(started)
		<-release
		return RunResult{Outcome: runedv1.OutcomeOK}
	}}}
	_, srv := serverWith(t, rec, fakeVerifier{scope: []string{"runs:run", "runs:kill"}})
	h := srv.Handler()

	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- doRun(t, h, runedv1.RunRequest{Folder: "demo", MessageBatch: "m"}) }()
	<-started

	stop := postJSON(t, h, "/v1/runs/stop", `{"folder":"demo"}`)
	var out runedv1.StopRunResponse
	json.Unmarshal(stop.Body.Bytes(), &out)
	if !out.Killed || out.RunID == "" {
		t.Fatalf("stop killed=%v run_id=%q want killed,non-empty", out.Killed, out.RunID)
	}
	close(release)
	<-done

	stop2 := postJSON(t, h, "/v1/runs/stop", `{"folder":"demo"}`)
	var out2 runedv1.StopRunResponse
	json.Unmarshal(stop2.Body.Bytes(), &out2)
	if out2.Killed {
		t.Fatalf("stop on idle folder killed=%v want false", out2.Killed)
	}
}

// TestHTTPRunStopMissingFolder: POST /v1/runs/stop with no folder is 400.
func TestHTTPRunStopMissingFolder(t *testing.T) {
	_, srv := serverWith(t, &killRecorder{}, fakeVerifier{scope: []string{"runs:kill"}})
	if got := postJSON(t, srv.Handler(), "/v1/runs/stop", `{}`); got.Code != 400 {
		t.Fatalf("stop missing folder = %d want 400", got.Code)
	}
}

// TestHTTPUnauthorizedWhenVerifyFails: a verifier that rejects the token gives
// 401 on gated routes; /health stays public (mounted pre-auth).
func TestHTTPUnauthorizedWhenVerifyFails(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	mgr := NewManager(db, &specRecorder{}, NewStaticBroker("jws", "jti"), ManagerConfig{Instance: "test", MaxConcurrent: 5})
	srv := NewServer(mgr, db, errVerifier{})
	h := srv.Handler()

	if got := req(t, h, "GET", "/health"); got.Code != 200 {
		t.Fatalf("health = %d want 200 (public, pre-auth)", got.Code)
	}
	if got := postJSON(t, h, "/v1/runs", `{"folder":"demo"}`); got.Code != 401 {
		t.Fatalf("run with bad token = %d want 401", got.Code)
	}
	if got := req(t, h, "GET", "/v1/sessions/recent?folder=demo"); got.Code != 401 {
		t.Fatalf("recent with bad token = %d want 401", got.Code)
	}
}

// errVerifier always rejects.
type errVerifier struct{}

func (errVerifier) Verify(*http.Request) (string, []string, string, error) {
	return "", nil, "", context.DeadlineExceeded
}

// TestHTTPRecentSessionsShape: GET /v1/sessions/recent returns rows newest-first
// with the full federated shape (group_folder + error), honoring ?n=.
func TestHTTPRecentSessionsShape(t *testing.T) {
	db, srv := serverWith(t, FakeRuntime{}, fakeVerifier{scope: []string{"sessions:read"}, folder: "demo"})
	for _, sid := range []string{"s1", "s2", "s3"} {
		id, _ := db.RecordSession("demo", sid)
		db.EndSession(id, sid, "ok", "", 2)
	}
	rec := req(t, srv.Handler(), "GET", "/v1/sessions/recent?n=2")
	if rec.Code != 200 {
		t.Fatalf("recent = %d want 200", rec.Code)
	}
	var out runedv1.RecentSessionsResponse
	json.Unmarshal(rec.Body.Bytes(), &out)
	if len(out.Sessions) != 2 {
		t.Fatalf("n=2 returned %d rows want 2", len(out.Sessions))
	}
	// newest first: s3 then s2.
	if out.Sessions[0].SessionID != "s3" || out.Sessions[1].SessionID != "s2" {
		t.Fatalf("recent order = %s,%s want s3,s2 (newest first)", out.Sessions[0].SessionID, out.Sessions[1].SessionID)
	}
	if out.Sessions[0].GroupFolder != "demo" {
		t.Fatalf("group_folder not populated: %+v", out.Sessions[0])
	}
}
