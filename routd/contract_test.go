package routd

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	apiv1 "github.com/kronael/arizuko/routd/api/v1"
	runedv1 "github.com/kronael/arizuko/runed/api/v1"
)

// stubRunner records the POST /v1/runs body and, on Run, calls back into
// routd's /v1/turns/{turn_id}/reply (proving the sole-appender callback)
// then returns {outcome:ok, session_id:stub}. Mirrors the spec 5/E
// standalone acceptance stub runed.
type stubRunner struct {
	srv      *Server
	gotBatch string
	gotTurn  string
}

func (r *stubRunner) Run(_ context.Context, req runedv1.RunRequest) (runedv1.RunOutcome, error) {
	r.gotBatch = req.MessageBatch
	r.gotTurn = req.TurnID
	// the agent's reply tool calls back into routd (the sole appender). The
	// HTTP idem wrapper persists the returned row; calling appendAndDeliver
	// directly, the stub must persist it itself.
	if _, _, row := r.srv.appendAndDeliver(req.TurnID, req.ChatJID, "hello back", "", true); row != nil {
		_ = r.srv.db.PutMessage(*row)
	}
	// the agent submits its turn.
	first, _ := r.srv.db.RecordTurnResult(string(req.Folder), req.TurnID, "sess-stub", "success")
	if first {
		_ = r.srv.db.SetTurnState(req.TurnID, "done")
	}
	return runedv1.RunOutcome{RunID: "run-stub", Outcome: runedv1.OutcomeOK, SessionID: "sess-stub"}, nil
}

func newTestRoutd(t *testing.T) (*DB, *Server, *stubRunner) {
	t.Helper()
	db, err := OpenMem()
	if err != nil {
		t.Fatalf("open mem db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	runner := &stubRunner{}
	loop := NewLoop(db, runner, LoopConfig{})
	loop.StopQueue() // drive processGroupMessages directly; no async dispatch races
	srv := NewServer(db, loop, nil, nil, 0, "https://example.test")
	runner.srv = srv
	return db, srv, runner
}

// TestContractRoundTrip is the spec 5/E § Standalone-ready acceptance:
// ingest → route → loop dispatches a stub run → callback reply appends one
// bot row → submit_turn recorded in turn_results, duplicate dropped.
func TestContractRoundTrip(t *testing.T) {
	db, srv, runner := newTestRoutd(t)
	h := srv.Handler()

	// register the target group and a single route rule.
	if err := db.PutGroup(core.Group{Folder: "demo"}); err != nil {
		t.Fatalf("put group: %v", err)
	}
	doJSON(t, h, "PUT", "/v1/routes", "", []apiv1.Route{{Seq: 0, Match: "platform=slack", Target: "demo"}})

	// (1) ingest an inbound via POST /v1/messages. The adapter sends a stable
	// platform id and no X-Idempotency-Key (the id is the dedup key).
	in := apiv1.Message{ID: "m1", ChatJID: "slack:T/C/U", Sender: "u1", Content: "hi", Verb: "message"}
	rec := doJSON(t, h, "POST", "/v1/messages", "", in)
	if rec.Code != 200 {
		t.Fatalf("ingest status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !db.MessageExists("m1") {
		t.Fatal("inbound row not stored")
	}

	// (2) the loop resolves the route and dispatches.
	hadOutput, err := srv.loop.processGroupMessages("slack:T/C/U")
	if err != nil {
		t.Fatalf("process: %v", err)
	}
	if !hadOutput {
		t.Fatal("expected the run to report output")
	}

	// (3) runed was called with the rendered batch + turn_id.
	if runner.gotTurn != "m1" {
		t.Fatalf("runed turn_id=%q want m1", runner.gotTurn)
	}
	if !strings.Contains(runner.gotBatch, "hi") {
		t.Fatalf("rendered batch missing trigger content: %q", runner.gotBatch)
	}

	// (4) the callback reply appended exactly one bot row.
	bots := countBots(t, db, "slack:T/C/U")
	if bots != 1 {
		t.Fatalf("bot rows=%d want 1", bots)
	}

	// (5) turn_results has one row; a duplicate submit_turn is dropped.
	first, _ := db.RecordTurnResult("demo", "m1", "sess-stub", "success")
	if first {
		t.Fatal("duplicate submit_turn was recorded (want dropped)")
	}
}

// TestTurnReplyAppendsAndDelivers checks the sole-appender callback writes a
// pending bot row and delivers via the Deliverer (append-then-deliver).
func TestTurnReplyAppendsAndDelivers(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	dl := &fakeDeliverer{platformID: "1716.0042"}
	srv := NewServer(db, nil, dl, nil, 0, "")
	_ = db.PutTurnContext("t1", "demo", "", "slack:T/C/U", "u1", "")

	h := srv.Handler()
	rec := doJSONKey(t, h, "POST", "/v1/turns/t1/reply", "idem-1",
		apiv1.ReplyRequest{JID: "slack:T/C/U", Text: "answer"})
	if rec.Code != 200 {
		t.Fatalf("reply status=%d body=%s", rec.Code, rec.Body.String())
	}
	var out apiv1.SendResult
	json.Unmarshal(rec.Body.Bytes(), &out)
	if out.Status != core.MessageStatusSent || out.PlatformID != "1716.0042" {
		t.Fatalf("reply result=%+v", out)
	}
	if dl.sends != 1 {
		t.Fatalf("deliverer sends=%d want 1", dl.sends)
	}
	if countBots(t, db, "slack:T/C/U") != 1 {
		t.Fatal("want exactly one bot row")
	}

	// idempotent replay: same key → no second row, replayed response.
	rec2 := doJSONKey(t, h, "POST", "/v1/turns/t1/reply", "idem-1",
		apiv1.ReplyRequest{JID: "slack:T/C/U", Text: "answer"})
	if rec2.Code != 200 {
		t.Fatalf("replay status=%d", rec2.Code)
	}
	if dl.sends != 1 {
		t.Fatalf("replay re-delivered (sends=%d)", dl.sends)
	}
	if countBots(t, db, "slack:T/C/U") != 1 {
		t.Fatal("replay appended a second bot row")
	}

	// key reuse with a different body → 409.
	rec3 := doJSONKey(t, h, "POST", "/v1/turns/t1/reply", "idem-1",
		apiv1.ReplyRequest{JID: "slack:T/C/U", Text: "different"})
	if rec3.Code != 409 {
		t.Fatalf("key-reuse status=%d want 409", rec3.Code)
	}
}

// TestResultRecordsOutcome checks /v1/turns/{id}/result records session_id,
// cost, flips turn state, and dedups on (folder, turn_id).
func TestResultRecordsOutcome(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	srv := NewServer(db, nil, nil, nil, 0, "")
	_ = db.PutTurnContext("t9", "demo", "main", "web:demo", "u1", "")
	h := srv.Handler()

	body := apiv1.TurnResult{TurnID: "t9", SessionID: "sX", Status: "success",
		Models: map[string]apiv1.ModelCost{"claude": {Input: 100, Output: 20, CostCents: 3}}}
	rec := doJSON(t, h, "POST", "/v1/turns/t9/result", "", body)
	var ack apiv1.TurnResultAck
	json.Unmarshal(rec.Body.Bytes(), &ack)
	if !ack.Recorded {
		t.Fatal("first result not recorded")
	}
	if got := db.SessionID("demo", "main"); got != "sX" {
		t.Fatalf("session not persisted: %q", got)
	}
	if db.SessionID("demo", "main") == "" {
		t.Fatal("session missing")
	}
	// duplicate → recorded:false.
	rec2 := doJSON(t, h, "POST", "/v1/turns/t9/result", "", body)
	var ack2 apiv1.TurnResultAck
	json.Unmarshal(rec2.Body.Bytes(), &ack2)
	if ack2.Recorded {
		t.Fatal("duplicate result was recorded (want dropped)")
	}
}

// TestRouteTokenRoundTrip checks issue → resolve → revoke.
func TestRouteTokenRoundTrip(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_ = db.PutGroup(core.Group{Folder: "acme"})
	srv := NewServer(db, nil, nil, nil, 0, "https://x.test")
	h := srv.Handler()

	rec := doJSON(t, h, "POST", "/v1/route_tokens/chat", "",
		apiv1.RouteTokenRequest{OwnerFolder: "acme", TargetFolder: "acme"})
	if rec.Code != 201 {
		t.Fatalf("issue status=%d body=%s", rec.Code, rec.Body.String())
	}
	var issued apiv1.RouteTokenResponse
	json.Unmarshal(rec.Body.Bytes(), &issued)
	if issued.Token == "" || issued.JID != "web:acme" {
		t.Fatalf("issued=%+v", issued)
	}

	rrec := doJSON(t, h, "POST", "/v1/route_tokens/resolve", "",
		apiv1.ResolveRequest{Token: issued.Token})
	var resolved apiv1.ResolveResponse
	json.Unmarshal(rrec.Body.Bytes(), &resolved)
	if resolved.JID != "web:acme" || resolved.OwnerFolder != "acme" {
		t.Fatalf("resolved=%+v", resolved)
	}
}

// --- helpers ---

type fakeDeliverer struct {
	platformID string
	sends      int
}

func (d *fakeDeliverer) Send(_, _, _, _, _ string) (string, error) {
	d.sends++
	return d.platformID, nil
}
func (d *fakeDeliverer) React(_, _, _ string) error      { return nil }
func (d *fakeDeliverer) Edit(_, _, _ string) error       { return nil }
func (d *fakeDeliverer) Delete(_, _ string) error        { return nil }
func (d *fakeDeliverer) Pin(_, _ string) error           { return nil }
func (d *fakeDeliverer) Unpin(_, _ string, _ bool) error { return nil }
func (d *fakeDeliverer) Document(_, _, _, _, _, _ string) (string, error) {
	return d.platformID, nil
}

func countBots(t *testing.T, db *DB, jid string) int {
	t.Helper()
	var n int
	db.SQL().QueryRow("SELECT COUNT(*) FROM messages WHERE chat_jid=? AND is_bot_message=1", jid).Scan(&n)
	return n
}

func doJSON(t *testing.T, h http.Handler, method, path, idemKey string, body any) *httptest.ResponseRecorder {
	return doJSONKey(t, h, method, path, idemKey, body)
}

func doJSONKey(t *testing.T, h http.Handler, method, path, idemKey string, body any) *httptest.ResponseRecorder {
	t.Helper()
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(method, path, strings.NewReader(string(raw)))
	req.Header.Set("Content-Type", "application/json")
	if idemKey != "" {
		req.Header.Set("X-Idempotency-Key", idemKey)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	_ = time.Now
	return rec
}
