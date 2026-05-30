package runed

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/routd"
)

// fedVerifier returns a fixed agent identity for the routd Handler so the
// read/manage endpoints authorize the federated StoreFns calls.
type fedVerifier struct{ folder string }

func (v fedVerifier) Verify(*http.Request) (string, []string, string, error) {
	return "agent:" + v.folder, []string{
		"chats:read:own_group", "messages:send:own_group",
		"routes:read:own_group", "routes:write:own_group",
	}, v.folder, nil
}

// fedHarness stands up a real routd Server (in-memory routd.db) behind an
// httptest server and a dockerRuntime whose Federator forwards to it — the
// exact runed→routd federation path the StoreFns use in production.
func fedHarness(t *testing.T) (*routd.DB, *dockerRuntime, func()) {
	t.Helper()
	rdb, err := routd.OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	srv := routd.NewServer(rdb, nil, nil, fedVerifier{folder: "demo"}, 0, "https://x")
	ts := httptest.NewServer(srv.Handler())
	d := &dockerRuntime{fed: NewFederator(ts.URL), db: nil}
	return rdb, d, func() { ts.Close(); rdb.Close() }
}

// TestStoreFnsMessageReadFederates: inspect_messages' MessagesBefore StoreFn
// returns the real rows routd holds, proving the read tool surface works
// through runed→routd in the split (the GAP 1.1 regression: StoreFns were
// nil, so the tool no-op'd).
func TestStoreFnsMessageReadFederates(t *testing.T) {
	rdb, d, done := fedHarness(t)
	defer done()
	if err := rdb.PutMessage(core.Message{ID: "m1", ChatJID: "slack:T/C/U", Sender: "u1",
		Content: "hello world", Timestamp: time.Now().Add(-time.Minute), Verb: "message"}); err != nil {
		t.Fatal(err)
	}
	if err := rdb.PutMessage(core.Message{ID: "m2", ChatJID: "slack:T/C/U", Sender: "u1",
		Content: "second", Timestamp: time.Now(), Verb: "message"}); err != nil {
		t.Fatal(err)
	}
	fns := d.storeFns(context.Background(), RunSpec{Folder: "demo", Token: "tok"})

	msgs, err := fns.MessagesBefore("slack:T/C/U", time.Time{}, 50)
	if err != nil {
		t.Fatalf("MessagesBefore: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("MessagesBefore returned %d rows, want 2 (real routd data)", len(msgs))
	}
	if msgs[0].Content != "hello world" || msgs[1].Content != "second" {
		t.Fatalf("rows out of chronological order: %q, %q", msgs[0].Content, msgs[1].Content)
	}

	hits, err := fns.FindMessages("hello", "", "", "", 10)
	if err != nil {
		t.Fatalf("FindMessages: %v", err)
	}
	if len(hits) != 1 || hits[0].ChatJID != "slack:T/C/U" {
		t.Fatalf("FindMessages returned %d hits, want 1 for 'hello'", len(hits))
	}
}

// TestStoreFnsRouteCRUDFederates: the route-management StoreFns add a route to
// routd and read it back — list_routes / add_route / get_route through the
// federation, plus DefaultFolderForJID resolving against the live route table.
func TestStoreFnsRouteCRUDFederates(t *testing.T) {
	rdb, d, done := fedHarness(t)
	defer done()
	_ = rdb
	fns := d.storeFns(context.Background(), RunSpec{Folder: "demo", Token: "tok"})

	id, err := fns.AddRoute(core.Route{Seq: 1, Match: "chat_jid=slack:T/C/*", Target: "demo"})
	if err != nil {
		t.Fatalf("AddRoute: %v", err)
	}
	if id == 0 {
		t.Fatal("AddRoute returned id 0")
	}
	routes := fns.ListRoutes("demo", false)
	if len(routes) != 1 || routes[0].Target != "demo" {
		t.Fatalf("ListRoutes returned %d routes, want 1 → demo", len(routes))
	}
	got, ok := fns.GetRoute(id)
	if !ok || got.Match != "chat_jid=slack:T/C/*" {
		t.Fatalf("GetRoute(%d) = %+v, ok=%v", id, got, ok)
	}
	if f := fns.DefaultFolderForJID("slack:T/C/U"); f != "demo" {
		t.Fatalf("DefaultFolderForJID resolved %q, want demo", f)
	}
	if !fns.JIDRoutedToFolder("slack:T/C/U", "demo") {
		t.Fatal("JIDRoutedToFolder should be true for a routed jid")
	}
	if err := fns.DeleteRoute(id); err != nil {
		t.Fatalf("DeleteRoute: %v", err)
	}
	if len(fns.ListRoutes("demo", false)) != 0 {
		t.Fatal("route survived DeleteRoute")
	}
}

// TestStoreFnsEngagementFederates: SetEngagement writes through to routd and
// EngagedFolder reads it back — the engage/disengage tool path.
func TestStoreFnsEngagementFederates(t *testing.T) {
	_, d, done := fedHarness(t)
	defer done()
	fns := d.storeFns(context.Background(), RunSpec{Folder: "demo", Token: "tok"})

	if err := fns.SetEngagement("slack:T/C/U", "", "demo", time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("SetEngagement: %v", err)
	}
	if f := fns.EngagedFolder("slack:T/C/U", ""); f != "demo" {
		t.Fatalf("EngagedFolder = %q, want demo", f)
	}
	// disengage: a zero deadline clears the window.
	if err := fns.SetEngagement("slack:T/C/U", "", "demo", time.Time{}); err != nil {
		t.Fatalf("SetEngagement(clear): %v", err)
	}
	if f := fns.EngagedFolder("slack:T/C/U", ""); f != "" {
		t.Fatalf("EngagedFolder after clear = %q, want empty", f)
	}
}

// TestStoreFnsTurnContextFromSpec: CurrentTriggerSender/CurrentTopic come from
// the RunSpec, not federation (runed owns the socket↔spawn binding).
func TestStoreFnsTurnContextFromSpec(t *testing.T) {
	_, d, done := fedHarness(t)
	defer done()
	fns := d.storeFns(context.Background(), RunSpec{Folder: "demo", Token: "tok",
		TriggerSender: "timed-cron", Topic: "t-42"})
	if s := fns.CurrentTriggerSender("demo"); s != "timed-cron" {
		t.Fatalf("CurrentTriggerSender = %q, want timed-cron", s)
	}
	if tp := fns.CurrentTopic("demo"); tp != "t-42" {
		t.Fatalf("CurrentTopic = %q, want t-42", tp)
	}
}
