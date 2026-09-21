package routd

// Does /openapi.json describe the body routd actually answers with?
//
// Nothing else asks. The advertised PATHS come off the mux and the request
// schemas off RowType, but a resreg.Handler returns `any`, so the verb cannot
// give the response shape — list → array<RowType>, GET → RowType — and 22
// mounted operations answer with something else (BUGS L120). The
// loudest are pinned here: GET /v1/routes answers []router.RouteView
// (annotated: mode, fires_turn, explain, shadowed_by), the task reads answer
// core.Task, which carries NO json tags, so its wire keys are Go field names
// while the doc promised snake_case columns, and GET /v1/groups answers a
// folder projection documented as the seven-column row.
//
// Each case serves a REAL request through the mux Server.Handler builds and
// compares the body to the schema the production emitter publishes for that
// operation. Deleting a Responses entry makes the case fail: the operation falls
// back to "OK" with no schema, the check finds none, and the non-vacuity count
// below drops.

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/resreg/resregtest"
)

// documentedBodyFixture seeds one row per read under test and returns routd's
// real mux. No verifier: the REST caller's folder claim is empty, which is the
// unrestricted operator read every case here wants.
func documentedBodyFixture(t *testing.T) *http.ServeMux {
	t.Helper()
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.PutGroup(core.Group{Folder: "hq"}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.AddRoute(core.Route{Seq: 1, Match: "platform=slack", Target: "hq"}); err != nil {
		t.Fatal(err)
	}
	next := time.Now().Add(time.Hour)
	if err := db.store().CreateTask(core.Task{
		ID: "task-doc-1", Owner: "hq", ChatJID: "slack:T/C/U", Prompt: "standup",
		Cron: "0 9 * * *", NextRun: &next, Status: core.TaskActive,
		Created: time.Now(), ContextMode: "group",
	}); err != nil {
		t.Fatal(err)
	}
	mux, ok := NewServer(db, nil, &recDeliverer{}, nil, 0, "").Handler().(*http.ServeMux)
	if !ok {
		t.Fatal("Server.Handler() is not *http.ServeMux — this guard cannot read its patterns")
	}
	return mux
}

// TestOpenAPIDescribesTheBodyItAnswers is the L120 pin. Each read below answers
// with something other than the RowType convention would predict.
func TestOpenAPIDescribesTheBodyItAnswers(t *testing.T) {
	mux := documentedBodyFixture(t)
	reads := []string{
		"/v1/routes",           // []router.RouteView, not []RoutesRow
		"/v1/tasks",            // []core.Task — Go field names, no json tags
		"/v1/tasks/task-doc-1", // core.Task
		"/v1/groups",           // the folder projection, not the 7-column row
	}
	checked := 0
	for _, path := range reads {
		if resregtest.AssertDocumentedResponse(t, "routd", mux, httptest.NewRequest("GET", path, nil)) {
			checked++
		}
	}
	// Non-vacuity: the check passes silently when an operation advertises no
	// schema, which is precisely the state L120 leaves an undeclared action in.
	// Without this count, deleting every Responses entry would look green.
	if checked != 4 {
		t.Fatalf("checked %d response schemas, want 4 — an operation stopped "+
			"advertising one, so its Responses declaration was dropped", checked)
	}
}
