package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/auth"
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
	r := stubRoutd(t, `{"tasks":[{"ID":"main/daily","Owner":"alice","ChatJID":"web:main","Prompt":"summarize","Cron":"0 9 * * *","Status":"active"}]}`)
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
	r := stubRoutd(t, `{"tasks":[]}`)
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
	r := stubRoutd(t, `{"tasks":[]}`)
	d := &dashServer{r: r, ks: auth.NewKeySet(nil)} // gate enforced

	req := httptest.NewRequest("GET", "/dash/timed/", nil)
	req.Header.Set("X-User-Groups", `["**"]`) // operator
	w := httptest.NewRecorder()
	d.handleDash(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200 for operator, got %d", w.Code)
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
