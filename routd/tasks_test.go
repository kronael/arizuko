package routd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/ipc"
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
	if got, _ := db.GetTask("due-1"); got.Status != "firing" {
		t.Errorf("due task status = %q want firing (claimed)", got.Status)
	}
	if got, _ := db.GetTask("future-1"); got.Status != core.TaskActive {
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
	got, _ := db.GetTask("rs-1")
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
	got, _ := db.GetTask("rs-once")
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

// TestTaskListEndpoint: GET /v1/tasks (tasks:read) returns every task; the
// ?status= filter narrows by status; a token without tasks:read is 403.
func TestTaskListEndpoint(t *testing.T) {
	db, h := authSrv(t, fakeVerifier{sub: "service:dashd", scope: []string{"tasks:read"}})
	seedTask(t, db, "tl-active", "main", "web:main", "live")
	s := store.New(db.SQL())
	if err := s.PutTaskRow(core.Task{
		ID: "tl-paused", Owner: "main", ChatJID: "web:main", Prompt: "off",
		Status: core.TaskPaused, Created: time.Now(),
	}); err != nil {
		t.Fatalf("seed paused: %v", err)
	}

	rec := doGet(t, h, "/v1/tasks")
	if rec.Code != 200 {
		t.Fatalf("GET /v1/tasks = %d want 200 body=%s", rec.Code, rec.Body.String())
	}
	var all struct {
		Tasks []core.Task `json:"tasks"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &all); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(all.Tasks) != 2 {
		t.Fatalf("tasks = %d want 2 (%+v)", len(all.Tasks), all.Tasks)
	}

	// ?status=active filters to just the active task.
	recA := doGet(t, h, "/v1/tasks?status=active")
	var act struct {
		Tasks []core.Task `json:"tasks"`
	}
	if err := json.Unmarshal(recA.Body.Bytes(), &act); err != nil {
		t.Fatalf("decode active: %v", err)
	}
	if len(act.Tasks) != 1 || act.Tasks[0].ID != "tl-active" {
		t.Fatalf("active filter = %+v want [tl-active]", act.Tasks)
	}

	// Gated by tasks:read.
	_, h2 := authSrv(t, fakeVerifier{sub: "user:u", scope: []string{"routes:read"}})
	if rec := doGet(t, h2, "/v1/tasks"); rec.Code != 403 {
		t.Fatalf("GET /v1/tasks without tasks:read = %d want 403", rec.Code)
	}
}

// TestTaskGetEndpoint: GET /v1/tasks/{id} (tasks:read) returns one task; a
// missing id is 404; a token without tasks:read is 403.
func TestTaskGetEndpoint(t *testing.T) {
	db, h := authSrv(t, fakeVerifier{sub: "service:dashd", scope: []string{"tasks:read"}})
	seedTask(t, db, "tg-1", "main", "web:main", "fetch me")

	rec := doGet(t, h, "/v1/tasks/tg-1")
	if rec.Code != 200 {
		t.Fatalf("GET /v1/tasks/tg-1 = %d want 200 body=%s", rec.Code, rec.Body.String())
	}
	var got core.Task
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.ID != "tg-1" || got.Prompt != "fetch me" {
		t.Fatalf("task = %+v want id=tg-1 prompt='fetch me'", got)
	}

	if rec := doGet(t, h, "/v1/tasks/nope"); rec.Code != 404 {
		t.Fatalf("GET missing task = %d want 404", rec.Code)
	}

	_, h2 := authSrv(t, fakeVerifier{sub: "user:u", scope: []string{"routes:read"}})
	if rec := doGet(t, h2, "/v1/tasks/tg-1"); rec.Code != 403 {
		t.Fatalf("GET /v1/tasks/{id} without tasks:read = %d want 403", rec.Code)
	}
}

// TestTaskRunLogsEndpoint: GET /v1/tasks/{id}/runs (tasks:read) returns that
// task's run history; a token without tasks:read is 403.
func TestTaskRunLogsEndpoint(t *testing.T) {
	db, h := authSrv(t, fakeVerifier{sub: "service:dashd", scope: []string{"tasks:read"}})
	seedTask(t, db, "trl-1", "main", "web:main", "logged")
	s := store.New(db.SQL())
	if err := s.RecordTaskRun(store.TaskRunLog{TaskID: "trl-1", Status: "ok", DurationMS: 7}); err != nil {
		t.Fatalf("record run: %v", err)
	}

	rec := doGet(t, h, "/v1/tasks/trl-1/runs")
	if rec.Code != 200 {
		t.Fatalf("GET /v1/tasks/trl-1/runs = %d want 200 body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Runs []ipc.TaskRunLog `json:"runs"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Runs) != 1 || resp.Runs[0].TaskID != "trl-1" || resp.Runs[0].DurationMS != 7 {
		t.Fatalf("runs = %+v want one trl-1/7 row", resp.Runs)
	}

	_, h2 := authSrv(t, fakeVerifier{sub: "user:u", scope: []string{"routes:read"}})
	if rec := doGet(t, h2, "/v1/tasks/trl-1/runs"); rec.Code != 403 {
		t.Fatalf("GET runs without tasks:read = %d want 403", rec.Code)
	}
}

// TestAllRunLogsEndpoint: GET /v1/tasks/runs (tasks:read) returns the
// cross-task run feed — rows from every task, newest first.
func TestAllRunLogsEndpoint(t *testing.T) {
	db, h := authSrv(t, fakeVerifier{sub: "service:dashd", scope: []string{"tasks:read"}})
	seedTask(t, db, "arl-a", "main", "web:main", "a")
	seedTask(t, db, "arl-b", "other", "web:other", "b")
	s := store.New(db.SQL())
	if err := s.RecordTaskRun(store.TaskRunLog{TaskID: "arl-a", Status: "ok"}); err != nil {
		t.Fatalf("record a: %v", err)
	}
	if err := s.RecordTaskRun(store.TaskRunLog{TaskID: "arl-b", Status: "error", Error: "boom"}); err != nil {
		t.Fatalf("record b: %v", err)
	}

	rec := doGet(t, h, "/v1/tasks/runs")
	if rec.Code != 200 {
		t.Fatalf("GET /v1/tasks/runs = %d want 200 body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Runs []ipc.TaskRunLog `json:"runs"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Runs) != 2 {
		t.Fatalf("runs = %d want 2 (cross-task) %+v", len(resp.Runs), resp.Runs)
	}
	// Newest first (id DESC): arl-b was recorded last.
	if resp.Runs[0].TaskID != "arl-b" {
		t.Errorf("first run = %q want arl-b (newest)", resp.Runs[0].TaskID)
	}

	_, h2 := authSrv(t, fakeVerifier{sub: "user:u", scope: []string{"routes:read"}})
	if rec := doGet(t, h2, "/v1/tasks/runs"); rec.Code != 403 {
		t.Fatalf("GET /v1/tasks/runs without tasks:read = %d want 403", rec.Code)
	}
}

// TestTaskPatchEndpoint: PATCH /v1/tasks/{id} (tasks:write) sets status and/or
// next_run; an empty body is 400; a token without tasks:write is 403.
func TestTaskPatchEndpoint(t *testing.T) {
	db, h := authSrv(t, fakeVerifier{sub: "service:dashd", scope: []string{"tasks:write"}})
	seedTask(t, db, "tp-1", "main", "web:main", "patch me")

	// PATCH status=paused flips the status; the response echoes the new task.
	rec := doJSON(t, h, "PATCH", "/v1/tasks/tp-1", "",
		map[string]string{"status": core.TaskPaused})
	if rec.Code != 200 {
		t.Fatalf("PATCH status = %d want 200 body=%s", rec.Code, rec.Body.String())
	}
	var got core.Task
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Status != core.TaskPaused {
		t.Errorf("response status = %q want paused", got.Status)
	}
	if stored, _ := db.GetTask("tp-1"); stored.Status != core.TaskPaused {
		t.Errorf("stored status = %q want paused", stored.Status)
	}

	// PATCH next_run sets the run time (run-now path).
	next := time.Now().Add(time.Minute).UTC().Format(time.RFC3339)
	rec2 := doJSON(t, h, "PATCH", "/v1/tasks/tp-1", "",
		map[string]string{"next_run": next})
	if rec2.Code != 200 {
		t.Fatalf("PATCH next_run = %d want 200 body=%s", rec2.Code, rec2.Body.String())
	}
	stored, _ := db.GetTask("tp-1")
	if stored.NextRun == nil || stored.NextRun.Format(time.RFC3339) != next {
		t.Errorf("stored next_run = %v want %s", stored.NextRun, next)
	}

	// Empty body → 400.
	if rec := doJSON(t, h, "PATCH", "/v1/tasks/tp-1", "", map[string]string{}); rec.Code != 400 {
		t.Fatalf("PATCH empty body = %d want 400", rec.Code)
	}

	// Gated by tasks:write.
	_, h2 := authSrv(t, fakeVerifier{sub: "user:u", scope: []string{"tasks:read"}})
	rec3 := doJSON(t, h2, "PATCH", "/v1/tasks/tp-1", "", map[string]string{"status": core.TaskPaused})
	if rec3.Code != 403 {
		t.Fatalf("PATCH without tasks:write = %d want 403", rec3.Code)
	}
}

// TestTaskDeleteEndpoint: DELETE /v1/tasks/{id} (tasks:write) removes the task
// and returns 204; a token without tasks:write is 403.
func TestTaskDeleteEndpoint(t *testing.T) {
	db, h := authSrv(t, fakeVerifier{sub: "service:dashd", scope: []string{"tasks:write"}})
	seedTask(t, db, "td-1", "main", "web:main", "delete me")

	req := httptest.NewRequest("DELETE", "/v1/tasks/td-1", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 204 {
		t.Fatalf("DELETE /v1/tasks/td-1 = %d want 204 body=%s", rec.Code, rec.Body.String())
	}
	if _, ok := db.GetTask("td-1"); ok {
		t.Fatal("task still present after DELETE")
	}

	// Gated by tasks:write.
	db2, h2 := authSrv(t, fakeVerifier{sub: "user:u", scope: []string{"tasks:read"}})
	seedTask(t, db2, "td-2", "main", "web:main", "kept")
	req2 := httptest.NewRequest("DELETE", "/v1/tasks/td-2", nil)
	rec2 := httptest.NewRecorder()
	h2.ServeHTTP(rec2, req2)
	if rec2.Code != 403 {
		t.Fatalf("DELETE without tasks:write = %d want 403", rec2.Code)
	}
	if _, ok := db2.GetTask("td-2"); !ok {
		t.Fatal("task removed despite 403")
	}
}
