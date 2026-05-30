package runed

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	runedv1 "github.com/kronael/arizuko/runed/api/v1"
	"github.com/kronael/arizuko/types"
)

// fakeVerifier returns a fixed (sub, scope, folder) for any request — stands
// in for offline JWKs verification in the auth tests.
type fakeVerifier struct {
	sub    string
	scope  []string
	folder string
}

func (v fakeVerifier) Verify(*http.Request) (string, []string, string, error) {
	return v.sub, v.scope, v.folder, nil
}

// killRecorder is a Runtime that records Kill calls.
type killRecorder struct {
	FakeRuntime
	killed int32
}

func (k *killRecorder) Kill(string) error { atomic.AddInt32(&k.killed, 1); return nil }

func serverWith(t *testing.T, rt Runtime, v Verifier) (*DB, *Server) {
	t.Helper()
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	mgr := NewManager(db, rt, NewStaticBroker("jws", "jti"), ManagerConfig{
		Scopes: []types.Scope{"messages:send:own_group"}, Instance: "test", MaxConcurrent: 5,
	})
	return db, NewServer(mgr, db, v)
}

func req(t *testing.T, h http.Handler, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	return rec
}

// TestKillRequiresScope: DELETE /v1/runs/{id} demands runs:kill — a token
// without it is 403 (spec 5/P § DELETE; bugs.md should-fix server.go:43).
func TestKillRequiresScope(t *testing.T) {
	rec := &killRecorder{}
	db, srv := serverWith(t, rec, fakeVerifier{scope: []string{"sessions:read"}}) // wrong scope
	_ = db.CreateSpawn(Spawn{RunID: "run_k", Folder: "demo", ContainerName: "c", State: "running"})
	h := srv.Handler()

	if got := req(t, h, "DELETE", "/v1/runs/run_k"); got.Code != 403 {
		t.Fatalf("kill without runs:kill = %d want 403", got.Code)
	}
	if atomic.LoadInt32(&rec.killed) != 0 {
		t.Fatal("container killed despite missing scope")
	}
}

// TestKillStopsContainer: a kill with runs:kill stops the container (Runtime.
// Kill) and records state=killed WITHOUT outcome=error (deliberate kill).
func TestKillStopsContainer(t *testing.T) {
	rec := &killRecorder{}
	db, srv := serverWith(t, rec, fakeVerifier{scope: []string{"runs:kill"}, folder: "demo"})
	_ = db.CreateSpawn(Spawn{RunID: "run_k", Folder: "demo", ContainerName: "c1", State: "running"})
	h := srv.Handler()

	if got := req(t, h, "DELETE", "/v1/runs/run_k"); got.Code != 200 {
		t.Fatalf("kill = %d want 200", got.Code)
	}
	if atomic.LoadInt32(&rec.killed) != 1 {
		t.Fatalf("Runtime.Kill called %d times, want 1", rec.killed)
	}
	sp, _ := db.GetSpawn("run_k")
	if sp.State != "killed" {
		t.Fatalf("state=%q want killed", sp.State)
	}
	if sp.Outcome == runedv1.OutcomeError {
		t.Fatalf("outcome=%q — a deliberate kill must NOT be error", sp.Outcome)
	}
}

// TestSessionsFolderBound: GET /v1/sessions is bounded by the token's
// arz/folder, NOT the ?folder= query param — a token cannot read another
// folder's history (bugs.md should-fix server.go:43).
func TestSessionsFolderBound(t *testing.T) {
	db, srv := serverWith(t, FakeRuntime{}, fakeVerifier{scope: []string{"sessions:read"}, folder: "alice"})
	// rows for two folders.
	a, _ := db.RecordSession("alice", "sa")
	db.EndSession(a, "sa", "ok", "", 1)
	b, _ := db.RecordSession("bob", "sb")
	db.EndSession(b, "sb", "ok", "", 1)
	h := srv.Handler()

	// even asking for bob, the token's folder (alice) wins.
	rec := req(t, h, "GET", "/v1/sessions?folder=bob")
	if rec.Code != 200 {
		t.Fatalf("sessions = %d want 200", rec.Code)
	}
	var out runedv1.SessionsResponse
	json.Unmarshal(rec.Body.Bytes(), &out)
	if len(out.Sessions) != 1 || out.Sessions[0].SessionID != "sa" {
		t.Fatalf("cross-folder read leaked: %+v (token folder=alice, asked bob)", out.Sessions)
	}
}

// TestSessionsRequiresScope: GET /v1/sessions without sessions:read is 403.
func TestSessionsRequiresScope(t *testing.T) {
	db, srv := serverWith(t, FakeRuntime{}, fakeVerifier{scope: []string{"runs:kill"}, folder: "alice"})
	_ = db
	if got := req(t, srv.Handler(), "GET", "/v1/sessions?folder=alice"); got.Code != 403 {
		t.Fatalf("sessions without sessions:read = %d want 403", got.Code)
	}
}
