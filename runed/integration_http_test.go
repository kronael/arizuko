package runed

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kronael/arizuko/container"
	"github.com/kronael/arizuko/core"
	runedv1 "github.com/kronael/arizuko/runed/api/v1"
)

// turnRecorder is a Runtime that captures the LAST Turn it was handed, and the
// ceiling on its ctx, then returns a scripted outcome — the integration seam
// for asserting routd's RunRequest reaches a rendered envelope end to end
// through the real HTTP handler + Manager, no docker.
type turnRecorder struct {
	last atomic.Pointer[Turn]
	// ttl is what was left of the ctx deadline when the run started; 0 means
	// the ctx carried none.
	ttl atomic.Int64
	res RunResult
}

func (r *turnRecorder) Run(ctx context.Context, turn Turn) RunResult {
	cp := turn
	r.last.Store(&cp)
	if dl, ok := ctx.Deadline(); ok {
		r.ttl.Store(int64(time.Until(dl)))
	}
	return r.res
}
func (*turnRecorder) Kill(string) error { return nil }

// TestHTTPRunThreadsFullTurn: POST /v1/runs over the real handler + Manager
// renders EVERY routd-supplied field into the envelope the substrate receives —
// folder, topic, channel, session, message, turn_id, trigger_sender, model,
// container_config, the capability grants, the egress allowlist and the
// deadline. A dropped field silently runs the agent mis-configured (wrong output
// style, wrong egress, missing grants), and the expectations below are literals
// so a renderer that changes its answer cannot agree with them.
func TestHTTPRunThreadsFullTurn(t *testing.T) {
	noHostSecrets(t)
	rec := &turnRecorder{res: RunResult{Outcome: runedv1.OutcomeOK}}
	// service:routd drives runs (folder=""), so any target folder is allowed.
	_, srv := serverWith(t, rec, fakeVerifier{scope: []string{"runs:run"}})
	h := srv.Handler()
	cfg := srv.mgr.cfg
	_, system := selectRenderPackage(t, cfg, "", "system-extra", "system-skill", "system skill")
	_, group := selectRenderPackage(t, cfg, "acme/eng", "group-extra", "group-skill", "group skill")

	got := doRun(t, h, runedv1.RunRequest{
		Folder: "acme/eng", Topic: "incident", ChatJID: "telegram:42",
		Channel: "telegram", SessionID: "resume-me", MessageBatch: "rendered",
		TriggerSender: "user:trig", CallerSub: "user:caller", TurnID: "wamid.Z",
		Model:           "claude-opus-4-8",
		ContainerConfig: map[string]any{"MaxChildren": 4},
		ShareReadOnly:   true,
		Egress:          true,
		WebPublish:      true,
		EgressAllowlist: []string{"api.acme.com", "github.com"},
	})
	if got.Code != 200 {
		t.Fatalf("run = %d want 200 (body=%s)", got.Code, got.Body.String())
	}
	turn := rec.last.Load()
	if turn == nil {
		t.Fatal("runtime never saw a Turn")
	}
	if turn.Folder != "acme/eng" {
		t.Fatalf("Folder=%q want acme/eng", turn.Folder)
	}
	if turn.Handle == "" {
		t.Fatal("Handle empty — the substrate has nothing to start, steer or kill by")
	}
	coreDir := selectedCore(t, cfg)
	for guest, source := range map[string]string{
		"/opt/arizuko/ant":                          coreDir,
		"/home/node/.claude/CLAUDE.md":              filepath.Join(coreDir, "CLAUDE.md"),
		"/home/node/.claude/skills/base":            filepath.Join(coreDir, "skills/base"),
		"/home/node/.claude/skills/system-skill":    filepath.Join(system, "skills/system-skill"),
		"/home/node/.claude/skills/group-skill":     filepath.Join(group, "skills/group-skill"),
		"/home/node/.claude/output-styles/brief.md": filepath.Join(coreDir, "output-styles/brief.md"),
	} {
		mount := mountAt(t, *turn, guest)
		if !mount.RO || mount.Host != source {
			t.Fatalf("immutable full Turn mount %s = %+v, want %s ro", guest, mount, source)
		}
	}
	captured, err := json.Marshal(map[string]any{"handle": turn.Handle, "folder": turn.Folder,
		"mounts": turn.Mounts, "env": turn.Env, "stdin": json.RawMessage(turn.Stdin), "egress": turn.Egress})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("captured Turn: %s", captured)

	// The stdin frame: the fields ant reads, plus the topic annotation the
	// renderer prefixes onto the prompt.
	frame := turnFrame(t, *turn)
	for key, want := range map[string]any{
		"chatJid":     "telegram:42",
		"groupFolder": "acme/eng",
		"topic":       "incident",
		"channelName": "telegram",
		"sessionId":   "resume-me",
		"messageId":   "wamid.Z",
		"sender":      "user:trig",
	} {
		if frame[key] != want {
			t.Errorf("stdin frame %s=%#v want %#v", key, frame[key], want)
		}
	}
	prompt, _ := frame["prompt"].(string)
	if !strings.HasSuffix(prompt, "\n\nrendered") {
		t.Errorf("prompt does not end with the rendered batch: %q", prompt)
	}
	if !strings.Contains(prompt, "Topic session: incident") {
		t.Errorf("topic annotation missing from the prompt: %q", prompt)
	}
	// caller_sub is the brokered token's subject and nothing in the envelope
	// reads it: it stays on the wire body (spec 6/41 §"What leaves the port").
	if strings.Contains(string(turn.Stdin), "user:caller") {
		t.Errorf("caller_sub reached the guest: %s", turn.Stdin)
	}

	// The process environment: the model override, and the query timeout derived
	// from the one deadline (60s ceiling → 60s floor, in ms).
	if turn.Env["ARIZUKO_MODEL"] != "claude-opus-4-8" {
		t.Errorf("Env[ARIZUKO_MODEL]=%q want claude-opus-4-8", turn.Env["ARIZUKO_MODEL"])
	}
	if turn.Env["ARIZUKO_QUERY_TIMEOUT_MS"] != "60000" {
		t.Errorf("Env[ARIZUKO_QUERY_TIMEOUT_MS]=%q want 60000", turn.Env["ARIZUKO_QUERY_TIMEOUT_MS"])
	}

	// The grants: share downgraded to RO, both web surfaces mounted, and the
	// egress grant appending "*" to the resolved allowlist.
	share := mountAt(t, *turn, "/var/lib/share")
	if !share.RO {
		t.Errorf("share_read_only did not downgrade /var/lib/share: %+v", share)
	}
	mountAt(t, *turn, "/home/node/public_html")
	mountAt(t, *turn, "/home/node/private_html")
	if strings.Join(turn.Egress, ",") != "api.acme.com,github.com,*" {
		t.Errorf("Egress=%v want [api.acme.com github.com *]", turn.Egress)
	}

	// The deadline: the Manager's, armed on ctx. 60s is the instance ceiling
	// renderWiring configures; container_config carried no Timeout.
	if ttl := time.Duration(rec.ttl.Load()); ttl <= 55*time.Second || ttl > 60*time.Second {
		t.Errorf("ctx deadline left %s, want just under 60s", ttl)
	}
}

func TestHTTPRunThreadsFullTurnRefusesUnsafePackages(t *testing.T) {
	for _, failure := range []string{"missing-core", "corrupt-selection", "collision", "duplicate"} {
		t.Run(failure, func(t *testing.T) {
			recorder := &turnRecorder{res: RunResult{Outcome: runedv1.OutcomeOK}}
			db, server := serverWith(t, recorder, fakeVerifier{scope: []string{"runs:run"}})
			cfg := server.mgr.cfg
			if failure == "duplicate" {
				selectRenderPackage(t, cfg, "g", "conflict", "base", "conflicting skill")
				write(t, filepath.Join(cfg.GroupsDir, "g/.claude/skills/base/.disabled"), "disabled")
			}
			lock, err := container.LockPackages(cfg.ProjectRoot)
			if err != nil {
				t.Fatal(err)
			}
			switch failure {
			case "missing-core":
				err = container.SelectPackages(cfg.ProjectRoot, "", nil)
			case "corrupt-selection":
				active := filepath.Join(cfg.ProjectRoot, "package-active/system")
				if err := os.Remove(active); err != nil {
					t.Fatal(err)
				}
				err = os.Symlink("../package-selections/system/"+strings.Repeat("0", 64), active)
			case "collision":
				write(t, filepath.Join(cfg.GroupsDir, "g/.claude/skills/base/SKILL.md"), "user skill")
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := lock.Close(); err != nil {
				t.Fatal(err)
			}
			for range 2 {
				response := doRun(t, server.Handler(), runedv1.RunRequest{Folder: "g", MessageBatch: "refuse"})
				var outcome runedv1.RunOutcome
				if err := json.Unmarshal(response.Body.Bytes(), &outcome); err != nil {
					t.Fatal(err)
				}
				if response.Code != http.StatusOK || outcome.Outcome != runedv1.OutcomeError ||
					outcome.Error == "" || outcome.Busy || !outcome.Terminal {
					t.Fatalf("refusal = %d, %+v", response.Code, outcome)
				}
				if recorder.last.Load() != nil {
					t.Fatal("runtime called after renderer refusal")
				}
				spawn, err := db.SpawnByRunID(t.Context(), outcome.RunID)
				if err != nil || spawn.State != "error" {
					t.Fatalf("refusal did not complete spawn: %+v, %v", spawn, err)
				}
				live, err := db.LiveSpawnInFolder(t.Context(), "g")
				if err != nil || live != nil {
					t.Fatalf("refusal retained slot: %+v, %v", live, err)
				}
			}
			sessions, err := db.RecentSessions(t.Context(), "g", 10)
			if err != nil || len(sessions) != 2 {
				t.Fatalf("refusal session history: %+v, %v", sessions, err)
			}
			for _, session := range sessions {
				if session.Result != runedv1.OutcomeError || session.EndedAt == "" || session.Error == "" {
					t.Fatalf("refusal did not close session: %+v", session)
				}
			}
			if _, err := os.Lstat(filepath.Join(cfg.GroupsDir, "g/.claude/settings.json")); !os.IsNotExist(err) {
				t.Fatalf("refusal wrote settings: %v", err)
			}
		})
	}
}

// mountAt returns the turn's mount at a guest path, failing when it is absent.
func mountAt(t *testing.T, turn Turn, guest string) core.Mount {
	t.Helper()
	for _, m := range turn.Mounts {
		if m.Container == guest {
			return m
		}
	}
	t.Fatalf("no mount at %s: %+v", guest, turn.Mounts)
	return core.Mount{}
}

func TestHTTPRunMissingFolder(t *testing.T) {
	rec := &turnRecorder{res: RunResult{Outcome: runedv1.OutcomeOK}}
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
	_, srv := serverWith(t, &turnRecorder{}, fakeVerifier{scope: []string{"runs:run"}})
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
	rec := &killRecorder{FakeRuntime: FakeRuntime{Fn: func(_ context.Context, _ Turn) RunResult {
		close(started)
		<-release
		return RunResult{Outcome: runedv1.OutcomeOK}
	}}}
	db, srv := serverWith(t, rec, fakeVerifier{scope: []string{"runs:run", "runs:kill"}})
	h := srv.Handler()

	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- doRun(t, h, runedv1.RunRequest{Folder: "demo", TurnID: "t1", MessageBatch: "m"}) }()
	<-started
	runID, _ := srv.mgr.ActiveRunID(t.Context(), "demo")
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
	if sp, _ := db.SpawnByRunID(t.Context(), runID); sp.State != "killed" {
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
	rec := &killRecorder{FakeRuntime: FakeRuntime{Fn: func(_ context.Context, _ Turn) RunResult {
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
	cfg, folders := renderWiring(t)
	mgr := NewManager(db, &turnRecorder{}, ManagerConfig{
		Instance: "test", MaxConcurrent: 5,
		Cfg: cfg, Folders: folders,
	})
	srv := NewServer(mgr, db, errVerifier{})
	h := srv.Handler()

	if got := req(t, h, "GET", "/health"); got.Code != 200 {
		t.Fatalf("health = %d want 200 (public, pre-auth)", got.Code)
	}
	if got := postJSON(t, h, "/v1/runs", `{"folder":"demo"}`); got.Code != 401 {
		t.Fatalf("run with bad token = %d want 401", got.Code)
	}
	if got := req(t, h, "GET", "/v1/spawn_sessions/recent?folder=demo"); got.Code != 401 {
		t.Fatalf("recent with bad token = %d want 401", got.Code)
	}
}

// errVerifier always rejects.
type errVerifier struct{}

func (errVerifier) Verify(*http.Request) (string, []string, string, error) {
	return "", nil, "", context.DeadlineExceeded
}

// TestHTTPRecentSessionsShape: GET /v1/spawn_sessions/recent returns rows newest-first
// with the full federated shape (group_folder + error), honoring ?n=.
func TestHTTPRecentSessionsShape(t *testing.T) {
	db, srv := serverWith(t, FakeRuntime{}, fakeVerifier{scope: []string{"sessions:read"}, folder: "demo"})
	for _, sid := range []string{"s1", "s2", "s3"} {
		id, _ := db.OpenSession(t.Context(), "demo", sid)
		if err := db.CloseSession(t.Context(), "demo", id, "ok", "", 2); err != nil {
			t.Fatalf("CloseSession: %v", err)
		}
	}
	rec := req(t, srv.Handler(), "GET", "/v1/spawn_sessions/recent?n=2")
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
