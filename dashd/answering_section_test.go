package main

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/routd"
)

// statusPage renders /dash/status/ as the operator and returns the HTML.
func statusPage(t *testing.T, d *dash) string {
	t.Helper()
	w := httptest.NewRecorder()
	newMux(d).ServeHTTP(w, asOperator(httptest.NewRequest("GET", "/dash/status/", nil)))
	if w.Code != 200 {
		t.Fatalf("GET /dash/status/ = %d; body=%q", w.Code, w.Body.String())
	}
	return w.Body.String()
}

// answeringDash stands a real routd up behind dashd, so the section is rendered
// from routd's own handler over routd's own store rather than from a stub that
// could invent a shape the daemon does not serve.
func answeringDash(t *testing.T) (*dash, *routd.DB) {
	t.Helper()
	db, err := routd.OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	// No verifier: routd answers dashd as it answers a service bearer with an
	// empty folder claim, which is what production sends.
	ts := httptest.NewServer(routd.NewServer(db, routd.NewLoop(db, nil, routd.LoopConfig{}),
		nil, nil, 0, "").Handler())
	t.Cleanup(ts.Close)
	return &dash{routd: db.Obj(), routdURL: ts.URL}, db
}

// TestStatus_AnsweringSectionSeparatesFaultFromConfiguration is the dashboard
// half of the question the maintainer could not settle: two chats wait by the
// same measure, and the page has to show that only one of them is broken.
func TestStatus_AnsweringSectionSeparatesFaultFromConfiguration(t *testing.T) {
	d, db := answeringDash(t)
	for _, f := range []string{"rhias/nemo", "demo"} {
		if err := db.PutGroup(core.Group{Folder: f}); err != nil {
			t.Fatal(err)
		}
	}
	seedRoute(t, db.Obj(), core.Route{Seq: 0,
		Match: "chat_jid=telegram:group/deaf verb=mention", Target: "rhias/nemo"})
	seedRoute(t, db.Obj(), core.Route{Seq: 1,
		Match: "chat_jid=telegram:group/broken", Target: "demo"})
	now := time.Now().UTC()
	seedRoutdMessage(t, db.Obj(), core.Message{ID: "d1", ChatJID: "telegram:group/deaf",
		Sender: "telegram:user/7", ChatName: "Deaf Room", Content: "anyone?",
		Timestamp: now.Add(-2 * time.Hour), Verb: "message"})
	seedRoutdMessage(t, db.Obj(), core.Message{ID: "b1", ChatJID: "telegram:group/broken",
		Sender: "telegram:user/8", ChatName: "Broken Room", Content: "hello?",
		Timestamp: now.Add(-2 * time.Hour), Verb: "message"})

	body := statusPage(t, d)
	if !strings.Contains(body, "<h2>Answering</h2>") {
		t.Fatalf("no answering section:\n%s", body)
	}
	for _, want := range []string{
		"Deaf Room", "Broken Room",
		// The fault and the configuration read differently on the page.
		`<span class="bad">no reply</span>`,
		`<span class="dim">no route</span>`,
		// The routed chat names the folder that owes the answer.
		`>demo</a>`,
		"2h",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("status page lacks %q:\n%s", want, body)
		}
	}
}

// TestStatus_AnsweringSectionCountsExpiredTurns — "turns are expiring" is the
// other fact no surface reported, and an expired turn must colour the banner.
func TestStatus_AnsweringSectionCountsExpiredTurns(t *testing.T) {
	d, db := answeringDash(t)
	if err := db.PutGroup(core.Group{Folder: "demo"}); err != nil {
		t.Fatal(err)
	}
	for id, state := range map[string]string{
		"t1": "done", "t2": "done", "t3": "expired",
	} {
		if _, err := db.PutTurnContext(id, "demo", "", "telegram:user/1", "u", ""); err != nil {
			t.Fatal(err)
		}
		if err := db.SetTurnState(id, state); err != nil {
			t.Fatal(err)
		}
	}
	body := statusPage(t, d)
	if !strings.Contains(body, "3 turns since") {
		t.Errorf("turn total missing:\n%s", body)
	}
	if !strings.Contains(body, "2 done") || !strings.Contains(body, "1 expired") {
		t.Errorf("turn outcome breakdown missing:\n%s", body)
	}
	// An expired turn is a warning, not an "ok".
	if !strings.Contains(body, `<div class="banner-warn">3 turns since`) {
		t.Errorf("expired turns did not colour the banner:\n%s", body)
	}
	// The separator is an entity, not text. Escaping the JOINED string instead
	// of each part renders a literal "&middot;" between every count.
	if strings.Contains(body, "&amp;middot;") {
		t.Errorf("the banner's separator is double-escaped:\n%s", body)
	}
}

// TestStatus_AnsweringSectionIsOperatorOnly — the turn counts are one
// instance-wide fold that cannot be narrowed per viewer, and a held chat
// belongs to no folder. A folder admin has a claim to neither.
func TestStatus_AnsweringSectionIsOperatorOnly(t *testing.T) {
	d, db := answeringDash(t)
	if err := db.PutGroup(core.Group{Folder: "demo"}); err != nil {
		t.Fatal(err)
	}
	seedRoutdMessage(t, db.Obj(), core.Message{ID: "h1", ChatJID: "telegram:user/7",
		Sender: "telegram:user/7", ChatName: "Secret Room", Content: "hi",
		Timestamp: time.Now().UTC(), Verb: "message"})

	if body := statusPage(t, d); !strings.Contains(body, "Secret Room") {
		t.Fatalf("operator cannot see the waiting chat:\n%s", body)
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/dash/status/", nil)
	r.Header.Set("X-User-Sub", "bob@x")
	r.Header.Set("X-User-Groups", `["demo"]`)
	newMux(d).ServeHTTP(w, r)
	body := w.Body.String()
	if strings.Contains(body, "<h2>Answering</h2>") || strings.Contains(body, "Secret Room") {
		t.Errorf("folder-scoped viewer was shown the answering section:\n%s", body)
	}
}

// TestStatus_AnsweringSectionSaysWhenRoutdIsUnreachable — an unreachable owner
// is an ERROR, never empty data (6/29). "Nobody is waiting" is the false calm
// this whole section exists to stop, so it must never be what a dead routd
// renders as.
func TestStatus_AnsweringSectionSaysWhenRoutdIsUnreachable(t *testing.T) {
	d, _ := answeringDash(t)
	d.routdURL = "http://127.0.0.1:1"
	body := statusPage(t, d)
	if !strings.Contains(body, "could not reach routd for answering health") {
		t.Errorf("unreachable routd is not reported:\n%s", body)
	}
	if strings.Contains(body, "Every chat's newest message has an answer after it.") {
		t.Error("a dead routd rendered as a healthy instance")
	}
}

// The session tally on the same page dropped an unreachable routd to a
// slog.Warn and rendered nothing, so an absent session table read as "no
// sessions" — a false statement an operator acts on, and exactly the failure
// mode spec 6/29 names. One page cannot hold two answers to "what does a dead
// routd look like" (BUGS L325).
func TestStatus_SessionCountsSayWhenRoutdIsUnreachable(t *testing.T) {
	d, _ := answeringDash(t)
	d.routdURL = "http://127.0.0.1:1"
	body := statusPage(t, d)
	if !strings.Contains(body, "could not reach routd for session counts") {
		t.Errorf("an unreachable routd left the session tally silent:\n%s", body)
	}
	// The page must not ALSO claim a healthy tally beside the banner.
	if strings.Contains(body, "<h2>Active sessions") {
		t.Error("a dead routd still rendered a session table")
	}
}
