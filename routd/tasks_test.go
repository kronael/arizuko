package routd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/store"
)

// doGet issues a GET against h (no body) and returns the recorder.
func doGet(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("GET", path, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// TestTasksDueEndpoint: GET /v1/tasks/due (tasks:read) atomically claims + returns
// the overdue active task from routd's OWN routd.db, flipping its status to
// 'firing' so a concurrent poller skips it.
func TestTasksDueEndpoint(t *testing.T) {
	db, h := authSrv(t, fakeVerifier{sub: "service:timed", scope: []string{"tasks:read"}})

	past := time.Now().Add(-time.Hour)
	s := store.New(db.SQL())
	if err := s.PutTaskRow(core.Task{
		ID: "due-1", Owner: "main", ChatJID: "web:main", Prompt: "ping",
		Cron: "0 9 * * *", NextRun: &past, Status: core.TaskActive, Created: time.Now(),
	}); err != nil {
		t.Fatalf("seed task: %v", err)
	}
	// A future task must NOT be returned.
	future := time.Now().Add(time.Hour)
	if err := s.PutTaskRow(core.Task{
		ID: "future-1", Owner: "main", ChatJID: "web:main", Prompt: "later",
		NextRun: &future, Status: core.TaskActive, Created: time.Now(),
	}); err != nil {
		t.Fatalf("seed future task: %v", err)
	}

	rec := doGet(t, h, "/v1/tasks/due")
	if rec.Code != 200 {
		t.Fatalf("GET /v1/tasks/due = %d want 200 body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Tasks []dueTask `json:"tasks"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Tasks) != 1 || resp.Tasks[0].ID != "due-1" {
		t.Fatalf("due tasks = %+v, want [due-1]", resp.Tasks)
	}
	if resp.Tasks[0].Prompt != "ping" {
		t.Errorf("due task prompt = %q want ping", resp.Tasks[0].Prompt)
	}
	// Claimed: the returned task is now 'firing', the future task still 'active'.
	if got, _ := db.SiblingGetTask("due-1"); got.Status != "firing" {
		t.Errorf("due task status = %q want firing (claimed)", got.Status)
	}
	if got, _ := db.SiblingGetTask("future-1"); got.Status != core.TaskActive {
		t.Errorf("future task status = %q want active (untouched)", got.Status)
	}
}

// TestTaskRunLogEndpoint: POST /v1/tasks/runlog (tasks:write) appends one
// task_run_logs row to routd's OWN routd.db, readable back via inspect.
func TestTaskRunLogEndpoint(t *testing.T) {
	db, h := authSrv(t, fakeVerifier{sub: "service:timed", scope: []string{"tasks:write"}})
	seedTask(t, db, "rl-1", "main", "web:main", "do it")

	rec := doJSON(t, h, "POST", "/v1/tasks/runlog", "", taskRunLogBody{
		TaskID: "rl-1", Status: "ok", DurationMS: 42})
	if rec.Code != 200 {
		t.Fatalf("POST /v1/tasks/runlog = %d want 200 body=%s", rec.Code, rec.Body.String())
	}
	logs := store.New(db.SQL()).TaskRunLogs("rl-1", 10)
	if len(logs) != 1 || logs[0].Status != "ok" || logs[0].DurationMS != 42 {
		t.Fatalf("task_run_logs = %+v, want one ok/42 row", logs)
	}
}

// TestTaskRescheduleRecurring: POST /v1/tasks/{id}/reschedule (tasks:write)
// with a non-empty next_run + status=active sets next_run and flips the task
// back to active — the recurring-task case (cron/interval re-arm).
func TestTaskRescheduleRecurring(t *testing.T) {
	db, h := authSrv(t, fakeVerifier{sub: "service:timed", scope: []string{"tasks:write"}})
	// Seed a 'firing' task (the state after GET /v1/tasks/due claimed it).
	s := store.New(db.SQL())
	now := time.Now()
	if err := s.PutTaskRow(core.Task{
		ID: "rs-1", Owner: "main", ChatJID: "web:main", Prompt: "tick",
		Cron: "0 9 * * *", NextRun: &now, Status: "firing", Created: now,
	}); err != nil {
		t.Fatalf("seed task: %v", err)
	}

	next := now.Add(24 * time.Hour).Format(time.RFC3339)
	rec := doJSON(t, h, "POST", "/v1/tasks/rs-1/reschedule", "",
		rescheduleBody{NextRun: next, Status: "active"})
	if rec.Code != 200 {
		t.Fatalf("reschedule = %d want 200 body=%s", rec.Code, rec.Body.String())
	}
	got, _ := db.SiblingGetTask("rs-1")
	if got.Status != "active" {
		t.Errorf("status = %q want active", got.Status)
	}
	if got.NextRun == nil || got.NextRun.Format(time.RFC3339) != next {
		t.Errorf("next_run = %v want %s", got.NextRun, next)
	}
}

// TestTaskRescheduleOneShot: an empty next_run + status=completed clears
// next_run (NULL) and marks the one-shot task completed.
func TestTaskRescheduleOneShot(t *testing.T) {
	db, h := authSrv(t, fakeVerifier{sub: "service:timed", scope: []string{"tasks:write"}})
	s := store.New(db.SQL())
	now := time.Now()
	if err := s.PutTaskRow(core.Task{
		ID: "rs-once", Owner: "main", ChatJID: "web:main", Prompt: "once",
		NextRun: &now, Status: "firing", Created: now,
	}); err != nil {
		t.Fatalf("seed task: %v", err)
	}

	rec := doJSON(t, h, "POST", "/v1/tasks/rs-once/reschedule", "",
		rescheduleBody{NextRun: "", Status: "completed"})
	if rec.Code != 200 {
		t.Fatalf("reschedule = %d want 200 body=%s", rec.Code, rec.Body.String())
	}
	got, _ := db.SiblingGetTask("rs-once")
	if got.Status != "completed" {
		t.Errorf("status = %q want completed", got.Status)
	}
	if got.NextRun != nil {
		t.Errorf("next_run = %v want nil (NULL)", got.NextRun)
	}
}

// TestTaskRescheduleRequiresWriteScope: a token without tasks:write is 403.
func TestTaskRescheduleRequiresWriteScope(t *testing.T) {
	_, h := authSrv(t, fakeVerifier{sub: "user:u", scope: []string{"tasks:read"}})
	rec := doJSON(t, h, "POST", "/v1/tasks/x/reschedule", "",
		rescheduleBody{NextRun: "", Status: "completed"})
	if rec.Code != 403 {
		t.Fatalf("reschedule without tasks:write = %d want 403", rec.Code)
	}
}

// TestTaskRescheduleMissingStatus: a body without status is 400.
func TestTaskRescheduleMissingStatus(t *testing.T) {
	_, h := authSrv(t, fakeVerifier{sub: "service:timed", scope: []string{"tasks:write"}})
	rec := doJSON(t, h, "POST", "/v1/tasks/x/reschedule", "",
		rescheduleBody{NextRun: "2026-01-01T00:00:00Z"})
	if rec.Code != 400 {
		t.Fatalf("reschedule without status = %d want 400", rec.Code)
	}
}

// TestTasksDueRequiresReadScope: a token without tasks:read is 403.
func TestTasksDueRequiresReadScope(t *testing.T) {
	_, h := authSrv(t, fakeVerifier{sub: "user:u", scope: []string{"routes:read"}})
	if rec := doGet(t, h, "/v1/tasks/due"); rec.Code != 403 {
		t.Fatalf("GET /v1/tasks/due without tasks:read = %d want 403", rec.Code)
	}
}

// TestTaskRunLogRequiresWriteScope: a token without tasks:write is 403.
func TestTaskRunLogRequiresWriteScope(t *testing.T) {
	_, h := authSrv(t, fakeVerifier{sub: "user:u", scope: []string{"tasks:read"}})
	rec := doJSON(t, h, "POST", "/v1/tasks/runlog", "", taskRunLogBody{TaskID: "x", Status: "ok"})
	if rec.Code != 403 {
		t.Fatalf("POST /v1/tasks/runlog without tasks:write = %d want 403", rec.Code)
	}
}
