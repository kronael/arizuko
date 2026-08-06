package routd

import (
	"context"
	"encoding/json"
	"fmt"
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
	busy     bool // when set, reject the run as busy (runed at capacity)
}

func (r *stubRunner) Run(_ context.Context, req runedv1.RunRequest) (runedv1.RunOutcome, error) {
	r.gotBatch = req.MessageBatch
	r.gotTurn = req.TurnID
	if r.busy {
		// runed did not admit (folder busy / at cap): a retryable reject, no
		// callback, no submit_turn.
		return runedv1.RunOutcome{Busy: true}, nil
	}
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
	doJSON(t, h, "PUT", "/v1/routes", "", map[string]any{"routes": []apiv1.Route{{Seq: 0, Match: "platform=slack", Target: "demo"}}})

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

// TestBusyOutcomeRetriesWithoutAdvanceOrBreaker: when runed rejects a run as
// busy (folder busy / at cap), routd must NOT advance the agent_cursor (so the
// batch is re-fed next poll), NOT surface an error (so the queue breaker never
// trips — busy ≠ failure), and leave turn_context 'running' (so the re-dispatch
// is live). Once capacity frees, the SAME turn_id re-dispatches and completes.
// Spec 5/E § routd↔runed interface — busy is retryable, not a route-miss and
// not a delivery failure.
func TestBusyOutcomeRetriesWithoutAdvanceOrBreaker(t *testing.T) {
	db, srv, runner := newTestRoutd(t)
	runner.busy = true
	h := srv.Handler()

	if err := db.PutGroup(core.Group{Folder: "demo"}); err != nil {
		t.Fatalf("put group: %v", err)
	}
	doJSON(t, h, "PUT", "/v1/routes", "", map[string]any{"routes": []apiv1.Route{{Seq: 0, Match: "platform=slack", Target: "demo"}}})
	in := apiv1.Message{ID: "m1", ChatJID: "slack:T/C/U", Sender: "u1", Content: "hi", Verb: "message"}
	doJSON(t, h, "POST", "/v1/messages", "", in)

	cursorBefore := db.GetAgentCursor("slack:T/C/U")

	// (1) busy dispatch: no error (no breaker), no output, cursor un-advanced.
	hadOutput, err := srv.loop.processGroupMessages("slack:T/C/U")
	if err != nil {
		t.Fatalf("busy surfaced as an error (would trip the queue breaker): %v", err)
	}
	if hadOutput {
		t.Fatal("busy reported output; want false (nothing ran)")
	}
	if got := db.GetAgentCursor("slack:T/C/U"); got != cursorBefore {
		t.Fatalf("cursor advanced on busy: %q != %q (batch dropped, not retried)", got, cursorBefore)
	}
	if tc, ok := db.GetTurnContext("m1"); !ok || tc.State != "running" {
		t.Fatalf("turn_context state=%q ok=%v want running (re-dispatchable)", tc.State, ok)
	}

	// (2) capacity frees; the next poll re-feeds and the SAME turn completes.
	runner.busy = false
	hadOutput, err = srv.loop.processGroupMessages("slack:T/C/U")
	if err != nil {
		t.Fatalf("retry process: %v", err)
	}
	if !hadOutput {
		t.Fatal("retry after busy produced no output; want the run to complete")
	}
	if runner.gotTurn != "m1" {
		t.Fatalf("retry dispatched turn_id=%q want m1 (same batch re-fed)", runner.gotTurn)
	}
	if db.GetAgentCursor("slack:T/C/U") == cursorBefore {
		t.Fatal("cursor did not advance after the successful retry")
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
	db.PutTurnContext("t1", "demo", "", "slack:T/C/U", "u1", "")

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

// TestReplyRequiresIdempotencyKey: reply/send/document are required=true on the
// idem ledger — a call with NO X-Idempotency-Key is 400 and must NOT execute
// (no bot row, no platform send). The key is the only dedup a runed retry has;
// if the required gate regressed to ledger-less at-least-once, a retried reply
// would double-send the agent's answer.
func TestReplyRequiresIdempotencyKey(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	dl := &fakeDeliverer{platformID: "9.9"}
	srv := NewServer(db, nil, dl, nil, 0, "")
	db.PutTurnContext("t1", "demo", "", "slack:T/C/U", "u1", "")
	h := srv.Handler()

	rec := doJSONKey(t, h, "POST", "/v1/turns/t1/reply", "", // no key
		apiv1.ReplyRequest{JID: "slack:T/C/U", Text: "answer"})
	if rec.Code != 400 {
		t.Fatalf("reply without idempotency key = %d want 400 (body=%s)", rec.Code, rec.Body.String())
	}
	if dl.sends != 0 {
		t.Fatalf("keyless reply delivered (sends=%d) — required gate must reject before execute", dl.sends)
	}
	if countBots(t, db, "slack:T/C/U") != 0 {
		t.Fatal("keyless reply appended a bot row despite the 400")
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
	db.PutTurnContext("t9", "demo", "main", "web:demo", "u1", "")
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

// TestRouteTokenRoundTrip checks a REST-minted token resolves. Resolution is
// deliberately NOT an HTTP leg: webd and proxyd are FS-mounted on routd.db and
// call ResolveRouteToken in-process, so that is what the round trip exercises
// (spec 5/W § Resolution).
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
	// Unified fold shape (5/16): {token,jid,url}, not the retired RouteTokenResponse.
	var issued struct {
		Token string `json:"token"`
		JID   string `json:"jid"`
	}
	json.Unmarshal(rec.Body.Bytes(), &issued)
	if issued.Token == "" || issued.JID != "web:acme" {
		t.Fatalf("issued=%+v", issued)
	}

	jid, owner, _, err := db.ResolveRouteToken(issued.Token)
	if err != nil || jid != "web:acme" || owner != "acme" {
		t.Fatalf("resolve: jid=%q owner=%q err=%v", jid, owner, err)
	}
}

// --- helpers ---

type fakeDeliverer struct {
	platformID string
	sends      int
	docErr     error
}

func (d *fakeDeliverer) Send(_, _, _, _, _, _ string) (string, error) {
	d.sends++
	return d.platformID, nil
}
func (d *fakeDeliverer) React(_, _, _ string) error      { return nil }
func (d *fakeDeliverer) Edit(_, _, _ string) error       { return nil }
func (d *fakeDeliverer) Delete(_, _ string) error        { return nil }
func (d *fakeDeliverer) Pin(_, _ string) error           { return nil }
func (d *fakeDeliverer) Unpin(_, _ string, _ bool) error { return nil }
func (d *fakeDeliverer) Typing(_ string, _ bool) error   { return nil }
func (d *fakeDeliverer) Document(_, _, _, _, _, _, _ string) (string, error) {
	if d.docErr != nil {
		return "", d.docErr
	}
	return d.platformID, nil
}
func (d *fakeDeliverer) SendVoice(_, _, _, _ string) (string, error) {
	return d.platformID, nil
}

// TestDeliverRow_SkipsBareFolderJid: a system/auto turn (e.g. /migrate) delivers
// its prose to a bare folder jid (no `platform:` prefix, no channel). deliverRow
// must SKIP the send — mark the row terminal, no retry — instead of ERROR-spamming
// a doomed Send that trips the per-folder breaker en masse on a MIGRATION_VERSION
// bump and blocks real replies for that folder. A real channel jid still delivers.
func TestDeliverRow_SkipsBareFolderJid(t *testing.T) {
	dl := &fakeDeliverer{platformID: "pid-1"}
	srv := &Server{deliver: dl}

	row := &core.Message{ID: "out-1", Status: core.MessageStatusPending}
	srv.deliverRow(TurnContext{Folder: "atlas"}, "atlas", row, "", "")
	if row.Status != core.MessageStatusSent {
		t.Fatalf("bare-folder jid: want Status=sent (skipped), got %q", row.Status)
	}
	if dl.sends != 0 {
		t.Fatalf("bare-folder jid must NOT call Send, got %d", dl.sends)
	}

	row2 := &core.Message{ID: "out-2", Status: core.MessageStatusPending}
	srv.deliverRow(TurnContext{Folder: "atlas"}, "slack:T/C/1", row2, "", "")
	if dl.sends != 1 || row2.Status != core.MessageStatusSent {
		t.Fatalf("channel jid: want 1 Send + sent, got sends=%d status=%q", dl.sends, row2.Status)
	}
}
func (d *fakeDeliverer) Post(_, _ string, _ []string) (string, error)       { return d.platformID, nil }
func (d *fakeDeliverer) Forward(_, _, _ string) (string, error)             { return d.platformID, nil }
func (d *fakeDeliverer) Quote(_, _, _ string) (string, error)               { return d.platformID, nil }
func (d *fakeDeliverer) Repost(_, _ string) (string, error)                 { return d.platformID, nil }
func (d *fakeDeliverer) Dislike(_, _ string) error                          { return nil }
func (d *fakeDeliverer) SetSuggestions(_ string, _ []core.PanePrompt) error { return nil }
func (d *fakeDeliverer) SetName(_, _ string) error                          { return nil }
func (d *fakeDeliverer) RoundDone(_, _, _, _ string) error                  { return nil }
func (d *fakeDeliverer) FetchHistory(_ string, _ time.Time, _ int) ([]byte, error) {
	return nil, fmt.Errorf("no history")
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
