package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/auth/authtest"
)

// stubRoutd serves GET /v1/tasks with the given JSON body, standing in for routd
// so the dashboard's task read resolves without a real router.
func stubRoutd(t *testing.T, tasksJSON string) *router {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/tasks" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(tasksJSON))
	}))
	t.Cleanup(srv.Close)
	return &router{base: srv.URL, http: &http.Client{Timeout: 2 * time.Second}, tz: "UTC"}
}

func TestDashOperatorOK(t *testing.T) {
	r := stubRoutd(t, `[{"ID":"main/daily","Owner":"alice","ChatJID":"web:main","Prompt":"summarize","Cron":"0 9 * * *","Status":"active"}]`)
	d := &dashServer{r: r, ks: nil} // nil ks → open, local-dev path

	req := httptest.NewRequest("GET", "/dash/timed/", nil)
	w := httptest.NewRecorder()
	d.handleDash(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "<table>") {
		t.Error("expected a task table in the rendered page")
	}
	if !strings.Contains(body, "main/daily") {
		t.Error("expected the task id rendered in the table")
	}
}

func TestDashNonOperatorForbidden(t *testing.T) {
	r := stubRoutd(t, `[]`)
	d := &dashServer{r: r, ks: auth.NewKeySet(nil)} // non-nil ks → gate enforced

	req := httptest.NewRequest("GET", "/dash/timed/", nil)
	req.Header.Set("X-User-Groups", `["main","main/eng"]`) // no `**`
	w := httptest.NewRecorder()
	d.handleDash(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("want 403 for non-operator, got %d", w.Code)
	}
}

func TestDashOperatorGroupAdmitted(t *testing.T) {
	// Both halves are required: the stamped group AND proxyd's service bearer.
	// The header alone is attacker input on any reachable port.
	ks, tok := authtest.Operator(t)
	d := &dashServer{r: stubRoutd(t, `[]`), ks: ks}
	req := httptest.NewRequest("GET", "/dash/timed/", nil)
	req.Header.Set("X-User-Groups", `["**"]`)
	req.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	d.handleDash(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200 for an operator behind the transit proof, got %d", w.Code)
	}
}

// The same request without the proof must be refused.
func TestDashOperatorHeaderWithoutProofRefused(t *testing.T) {
	d := &dashServer{r: stubRoutd(t, `[]`), ks: auth.NewKeySet(nil)}
	req := httptest.NewRequest("GET", "/dash/timed/", nil)
	req.Header.Set("X-User-Groups", `["**"]`)
	w := httptest.NewRecorder()
	d.handleDash(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("forged operator header = %d, want 403", w.Code)
	}
}

// TestRenderDashFlags asserts the spec's lag and stuck-fire rendering: an active
// task whose next_run passed >2 ticks ago is "lagging"; a row stuck in 'firing'
// >2 ticks is "stuck".
func TestRenderDashFlags(t *testing.T) {
	now := time.Date(2026, 6, 16, 12, 0, 0, 0, time.UTC)
	past := now.Add(-5 * time.Minute)
	tasks := []taskRow{
		{ID: "lag-task", Status: "active", NextRun: &past},
		{ID: "stuck-task", Status: "firing", NextRun: &past},
	}
	body := string(renderDash(tasks, loopState{}, now))
	if !strings.Contains(body, "lagging") {
		t.Error("expected lag flag for an active past-due task")
	}
	if !strings.Contains(body, "stuck") {
		t.Error("expected stuck flag for a firing past-due task")
	}
	if !strings.Contains(body, "No tick yet") {
		t.Error("expected zero-value loop state to render 'No tick yet'")
	}
}

// roundDur is what the operator reads on /dash/timed/ for every task's last
// run. Sub-minute durations round to seconds so a 3s task does not read as
// "0s", and anything longer rounds to minutes so a 41-minute run does not
// print six significant figures.
func TestRoundDurKeepsSecondsUnderAMinute(t *testing.T) {
	cases := map[time.Duration]string{
		0:                               "0s",
		500 * time.Millisecond:          "1s",
		400 * time.Millisecond:          "0s",
		3 * time.Second:                 "3s",
		59 * time.Second:                "59s",
		time.Minute:                     "1m0s",
		90 * time.Second:                "2m0s",
		41*time.Minute + 20*time.Second: "41m0s",
		2*time.Hour + 30*time.Minute:    "2h30m0s",
	}
	for in, want := range cases {
		if got := roundDur(in); got != want {
			t.Errorf("roundDur(%s) = %q, want %q", in, got, want)
		}
	}
}

// loopSection is the one line on /dash/timed/ that tells an operator whether
// the fire loop is alive. Every branch of it decides what they see when
// something is wrong.
func TestLoopSectionRendersEachHealthState(t *testing.T) {
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)

	// A daemon that has not ticked yet must SAY so rather than render a zero
	// time, which reads as "last tick 0001-01-01" and looks like a crash.
	if got := string(loopSection(loopState{}, now)); !strings.Contains(got, "No tick yet") {
		t.Errorf("a zero LastTick rendered %q", got)
	}

	healthy := loopSection(loopState{LastTick: now.Add(-30 * time.Second), Fired: 4}, now)
	got := string(healthy)
	for _, want := range []string{"dot-ok", "fired 4", "2026-08-27T11:59:30Z", "30s ago"} {
		if !strings.Contains(got, want) {
			t.Errorf("a healthy tick is missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "dot-err") {
		t.Errorf("a healthy tick rendered the error dot:\n%s", got)
	}

	// An error replaces the fired count: the operator needs the reason, and a
	// green dot beside a broken claim is the worst of both.
	failed := string(loopSection(loopState{
		LastTick: now.Add(-2 * time.Minute), Fired: 9, Err: "claim: database is locked",
	}, now))
	if !strings.Contains(failed, "dot-err") {
		t.Errorf("a loop error rendered no error dot:\n%s", failed)
	}
	if !strings.Contains(failed, "database is locked") {
		t.Errorf("the error text is missing:\n%s", failed)
	}
	if strings.Contains(failed, "fired 9") {
		t.Errorf("the fired count survived beside the error:\n%s", failed)
	}
}

// The error string comes from routd over HTTP. Rendering it unescaped puts
// whatever a failing claim reports straight into the operator's page.
func TestLoopSectionEscapesTheErrorText(t *testing.T) {
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	got := string(loopSection(loopState{
		LastTick: now.Add(-time.Minute),
		Err:      `<script>alert(1)</script>`,
	}, now))
	if strings.Contains(got, "<script>") {
		t.Errorf("the loop error reached the page unescaped:\n%s", got)
	}
	if !strings.Contains(got, "&lt;script&gt;") {
		t.Errorf("the error text was dropped instead of escaped:\n%s", got)
	}
}

// truncate bounds a cell so one long prompt cannot blow out the table. It must
// leave a short value alone rather than always append the ellipsis.
func TestTruncateOnlyCutsWhatIsTooLong(t *testing.T) {
	cases := []struct {
		in   string
		n    int
		want string
	}{
		{"short", 10, "short"},
		{"exactly-10", 10, "exactly-10"},
		{"eleven-char", 10, "eleven-cha…"},
		{"", 5, ""},
	}
	for _, c := range cases {
		if got := truncate(c.in, c.n); got != c.want {
			t.Errorf("truncate(%q, %d) = %q, want %q", c.in, c.n, got, c.want)
		}
	}
}
