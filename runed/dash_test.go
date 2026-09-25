package runed

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/substrate"
)

// The dashboard states what the substrate delivers and why less when less: the
// operator's surface for an egress downgrade (spec 6/25 §3). Under the one
// substrate a downgrade cannot happen — NewQemuRuntime refuses to start instead
// of delivering less — so what this pins is that the page renders the delivered
// profile at all, and the Reason line when one is set.
func TestDashboardShowsSubstrate(t *testing.T) {
	s, _, h := dashSrv(t)
	s.SetSubstrate(substrate.Effective{
		Declared:  qemuProfile,
		Delivered: qemuProfile,
		Reason:    "the guest reaches the proxy and nothing else",
	})
	body := dashGet(h, "", `["**"]`).Body.String()
	for _, want := range []string{"Substrate: kvm", "egress domain", "declared domain",
		"the guest reaches the proxy and nothing else"} {
		if !strings.Contains(body, want) {
			t.Errorf("the overview does not render %q", want)
		}
	}
}

// dashSrv builds a runed Server with its dashboard mounted.
func dashSrv(t *testing.T) (*Server, *DB, http.Handler) {
	t.Helper()
	db, mgr := newMgr(t, FakeRuntime{}, 5)
	s := NewServer(mgr, db, nil)
	return s, db, s.Handler()
}

func dashGet(h http.Handler, query, groups string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, "http://k.example/dash/runed/"+query, nil)
	if groups != "" {
		r.Header.Set("X-User-Groups", groups)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func dashPost(h http.Handler, path, form, groups string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "http://k.example"+path, strings.NewReader(form))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Origin", "http://k.example")
	if groups != "" {
		r.Header.Set("X-User-Groups", groups)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// runed serves its own dashboard, and the kill control the proxyd route takes
// over from dashd along with the prefix (6/28, 6/30).
func TestDashboardAndItsControlAreServedByRuned(t *testing.T) {
	_, db, h := dashSrv(t)
	_ = db.CreateSpawn(t.Context(), Spawn{RunID: "r1", Folder: "acme", ContainerName: "c1",
		State: "running", SessionID: "s"})

	w := dashGet(h, "", `["**"]`)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /dash/runed/ = %d, want 200", w.Code)
	}
	body := w.Body.String()
	for _, want := range []string{"runed", "Live spawns", "r1", "acme", "/dash/services/"} {
		if !strings.Contains(body, want) {
			t.Errorf("the overview does not render %q", want)
		}
	}
	// The kill control must answer, or the route takeover leaves a dead button.
	if got := dashPost(h, "/dash/runed/kill", "run_id=r1", `["**"]`); got.Code == http.StatusNotFound {
		t.Error("POST /dash/runed/kill is routed to runed and nothing answers it")
	}
}

// Kill stops the run through the Manager — the same path DELETE /v1/runs/{id}
// uses, not a second one.
func TestDashKillStopsTheRun(t *testing.T) {
	_, db, h := dashSrv(t)
	_ = db.CreateSpawn(t.Context(), Spawn{RunID: "r1", Folder: "acme", ContainerName: "c1",
		State: "running", SessionID: "s"})

	if w := dashPost(h, "/dash/runed/kill", "run_id=r1", `["**"]`); w.Code != http.StatusSeeOther {
		t.Fatalf("kill = %d, want 303", w.Code)
	}
	sp, err := db.SpawnByRunID(t.Context(), "r1")
	if err != nil {
		t.Fatal(err)
	}
	if sp.State != "killed" {
		t.Errorf("state = %q after kill, want killed", sp.State)
	}
}

// Only a live run offers a kill button: killing an exited run is a no-op 200,
// so rendering the control on one invites an operator to press a button that
// does nothing and reports success.
func TestOnlyLiveRunsOfferKill(t *testing.T) {
	live := runTable([]Spawn{{RunID: "r1", Folder: "acme", State: "running"}})
	if !strings.Contains(live, "/dash/runed/kill") {
		t.Error("a running run has no kill button")
	}
	for _, state := range []string{"exited", "killed"} {
		got := runTable([]Spawn{{RunID: "r1", Folder: "acme", State: state}})
		if strings.Contains(got, "/dash/runed/kill") {
			t.Errorf("state %q offers kill; killing it is a no-op that reports success", state)
		}
	}
	if !strings.Contains(runTable(nil), "No runs") {
		t.Error("the empty table has no clean-state line")
	}
}

// The state filter narrows the table, and every state stays selectable even
// when nothing is in it — a filter that vanishes when empty is missing exactly
// when an operator reaches for it.
func TestStateFilterNarrowsAndStaysComplete(t *testing.T) {
	_, db, h := dashSrv(t)
	_ = db.CreateSpawn(t.Context(), Spawn{RunID: "live", Folder: "acme", ContainerName: "c1", State: "running"})
	_ = db.CreateSpawn(t.Context(), Spawn{RunID: "done", Folder: "acme", ContainerName: "c2", State: "exited"})

	running := dashGet(h, "?state=running", `["**"]`).Body.String()
	if !strings.Contains(running, "live") || strings.Contains(running, `<code>done</code>`) {
		t.Error("state=running did not narrow the table to running spawns")
	}
	for _, st := range []string{"queued", "killed"} {
		if !strings.Contains(running, "?state="+st) {
			t.Errorf("the %q filter link is missing", st)
		}
	}
}

// The dashboard is operator-only when armed, open in local dev.
func TestRunedDashboardGate(t *testing.T) {
	s, _, h := dashSrv(t)
	if w := dashGet(h, "", ""); w.Code != http.StatusOK {
		t.Errorf("nil keyset = %d, want 200", w.Code)
	}
	s.SetDashKeySet(&auth.KeySet{})
	if w := dashGet(h, "", `["acme"]`); w.Code != http.StatusForbidden {
		t.Errorf("folder-scoped caller = %d, want 403", w.Code)
	}
	if w := dashPost(h, "/dash/runed/kill", "run_id=r1", `["acme"]`); w.Code != http.StatusForbidden {
		t.Errorf("folder-scoped kill = %d, want 403", w.Code)
	}
}

// A folder name reaches HTML from operator-supplied data.
func TestRunTableEscapes(t *testing.T) {
	got := runTable([]Spawn{{RunID: `<script>x</script>`, Folder: `a"b`, State: "exited"}})
	if strings.Contains(got, "<script>") {
		t.Error("a run id rendered as live markup")
	}
}
