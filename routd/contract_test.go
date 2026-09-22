package routd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/chanlib"
	"github.com/kronael/arizuko/core"
	apiv1 "github.com/kronael/arizuko/routd/api/v1"
	runedv1 "github.com/kronael/arizuko/runed/api/v1"
	"github.com/kronael/arizuko/store"
)

// stubRunner records the POST /v1/runs body and, on Run, replies through
// routd's append-and-deliver (the sole appender) then returns
// {outcome:ok, session_id:stub}. Mirrors the spec 5/E standalone acceptance
// stub runed.
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
	// the agent's reply lands through routd's append-and-deliver (the sole
	// appender).
	r.srv.appendAndDeliver(req.TurnID, req.ChatJID, "hello back")
	// the agent submits its turn.
	first, _ := r.srv.db.RecordTurnResult(string(req.Folder), req.TurnID, "sess-stub", "success", "")
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
	if !messageExists(db, "slack:T/C/U", "m1") {
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
	first, _ := db.RecordTurnResult("demo", "m1", "sess-stub", "success", "")
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

	// (2) the very next poll must NOT re-dispatch: the busy backoff defers the
	// re-feed, so the expensive pre-dispatch setup does not run every 2s
	// (BUGS L55 — 4,704 dispatches for 18 spawns after a krons restart).
	runner.gotTurn = ""
	hadOutput, err = srv.loop.processGroupMessages("slack:T/C/U")
	if err != nil {
		t.Fatalf("deferred process: %v", err)
	}
	if hadOutput || runner.gotTurn != "" {
		t.Fatalf("re-poll inside the busy backoff dispatched turn_id=%q; want no dispatch", runner.gotTurn)
	}
	if got := db.GetAgentCursor("slack:T/C/U"); got != cursorBefore {
		t.Fatalf("cursor advanced during backoff: %q != %q", got, cursorBefore)
	}

	// (3) capacity frees; after the backoff window the SAME turn completes and
	// the backoff entry is cleared by the cursor advance.
	expireBusyWait(t, srv.loop, "slack:T/C/U")
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
	if _, ok := srv.loop.busyWait.Load("slack:T/C/U"); ok {
		t.Fatal("busy backoff entry survived the successful retry; want cleared on advance")
	}
}

// expireBusyWait rewinds a chat's busy-backoff window so a test can retry
// without sleeping.
func expireBusyWait(t *testing.T, l *Loop, chatJID string) {
	t.Helper()
	v, ok := l.busyWait.Load(chatJID)
	if !ok {
		t.Fatalf("no busy backoff recorded for %s", chatJID)
	}
	st := v.(busyState)
	st.until = time.Now().Add(-time.Millisecond)
	l.busyWait.Store(chatJID, st)
}

// TestBusyBackoffGrowsAndNewMessageBypasses: a consecutive refusal of the
// same batch doubles the backoff; a NEW inbound message clears it so fresh
// user activity (including slash commands, which steer before dispatch) is
// never delayed behind a starved batch.
func TestBusyBackoffGrowsAndNewMessageBypasses(t *testing.T) {
	db, srv, runner := newTestRoutd(t)
	runner.busy = true
	h := srv.Handler()

	if err := db.PutGroup(core.Group{Folder: "demo"}); err != nil {
		t.Fatalf("put group: %v", err)
	}
	doJSON(t, h, "PUT", "/v1/routes", "", map[string]any{"routes": []apiv1.Route{{Seq: 0, Match: "platform=slack", Target: "demo"}}})
	doJSON(t, h, "POST", "/v1/messages", "", apiv1.Message{ID: "m1", ChatJID: "slack:T/C/U", Sender: "u1", Content: "hi", Verb: "message"})

	// First refusal: backoff at base.
	if _, err := srv.loop.processGroupMessages("slack:T/C/U"); err != nil {
		t.Fatalf("process: %v", err)
	}
	st := loadBusyState(t, srv.loop, "slack:T/C/U")
	if st.delay != busyBackoffBase {
		t.Fatalf("first refusal delay=%v want %v", st.delay, busyBackoffBase)
	}

	// Second refusal of the SAME batch (window expired): backoff doubles.
	expireBusyWait(t, srv.loop, "slack:T/C/U")
	if _, err := srv.loop.processGroupMessages("slack:T/C/U"); err != nil {
		t.Fatalf("process: %v", err)
	}
	st = loadBusyState(t, srv.loop, "slack:T/C/U")
	if st.delay != 2*busyBackoffBase {
		t.Fatalf("second refusal delay=%v want %v", st.delay, 2*busyBackoffBase)
	}

	// A new inbound message bypasses the standing backoff: the next poll
	// dispatches immediately (and, still refused, restarts the backoff at base).
	doJSON(t, h, "POST", "/v1/messages", "", apiv1.Message{ID: "m2", ChatJID: "slack:T/C/U", Sender: "u1", Content: "again", Verb: "message"})
	runner.gotTurn = ""
	if _, err := srv.loop.processGroupMessages("slack:T/C/U"); err != nil {
		t.Fatalf("process: %v", err)
	}
	if runner.gotTurn != "m2" {
		t.Fatalf("new message did not bypass the backoff: dispatched turn_id=%q want m2", runner.gotTurn)
	}
	st = loadBusyState(t, srv.loop, "slack:T/C/U")
	if st.delay != busyBackoffBase || st.lastMsgID != "m2" {
		t.Fatalf("backoff after bypass delay=%v lastMsgID=%q; want base delay keyed on m2", st.delay, st.lastMsgID)
	}
}

// A RUNNING turn writes its own rows — progress notes, replies — and those must
// not clear the backoff that exists because that turn is running. One cron fire
// re-dispatched itself fifteen times this way, each attempt paying for a
// capability resolve, an MCP socket and a prompt build (BUGS L298).
func TestABotRowDoesNotClearTheBusyBackoff(t *testing.T) {
	db, srv, runner := newTestRoutd(t)
	runner.busy = true
	h := srv.Handler()

	if err := db.PutGroup(core.Group{Folder: "demo"}); err != nil {
		t.Fatalf("put group: %v", err)
	}
	doJSON(t, h, "PUT", "/v1/routes", "", map[string]any{"routes": []apiv1.Route{
		{Seq: 0, Match: "platform=slack", Target: "demo"},
	}})
	doJSON(t, h, "POST", "/v1/messages", "", apiv1.Message{
		ID: "m1", ChatJID: "slack:T/C/U", Sender: "u1", Content: "run the thing", Verb: "message",
	})
	if _, err := srv.loop.processGroupMessages("slack:T/C/U"); err != nil {
		t.Fatalf("process: %v", err)
	}
	st := loadBusyState(t, srv.loop, "slack:T/C/U")
	if st.lastMsgID != "m1" {
		t.Fatalf("the backoff is keyed on %q, want the user row m1", st.lastMsgID)
	}

	// The turn runs and writes a bot row into the same chat, the way a reply
	// lands: routd appends it itself, so it arrives through the store.
	if err := db.PutMessage(core.Message{
		ID: "b1", ChatJID: "slack:T/C/U", Sender: "demo", Name: "demo",
		Content: "working on it", Timestamp: time.Now(), Verb: "message",
		BotMsg: true, FromMe: true,
	}); err != nil {
		t.Fatalf("append the bot row: %v", err)
	}
	runner.gotTurn = ""
	if _, err := srv.loop.processGroupMessages("slack:T/C/U"); err != nil {
		t.Fatalf("process after the bot row: %v", err)
	}
	if runner.gotTurn != "" {
		t.Errorf("the bot row cleared the backoff and re-dispatched turn_id=%q", runner.gotTurn)
	}
	if st := loadBusyState(t, srv.loop, "slack:T/C/U"); st.delay != busyBackoffBase {
		t.Errorf("the backoff restarted at %v; the bot row must leave it alone", st.delay)
	}
}

// loadBusyState fetches a chat's recorded busy backoff, failing when absent.
func loadBusyState(t *testing.T, l *Loop, chatJID string) busyState {
	t.Helper()
	v, ok := l.busyWait.Load(chatJID)
	if !ok {
		t.Fatalf("no busy backoff recorded for %s", chatJID)
	}
	return v.(busyState)
}

// TestTurnReplyAppendsAndDelivers checks the sole appender writes the bot row
// and delivers via the Deliverer, stamping the platform id on the row.
func TestTurnReplyAppendsAndDelivers(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	dl := &fakeDeliverer{platformID: "1716.0042"}
	srv := NewServer(db, nil, dl, nil, 0, "")
	db.PutTurnContext("t1", "demo", "", "slack:T/C/U", "u1", "")

	srv.appendAndDeliver("t1", "slack:T/C/U", "answer")
	if dl.sends != 1 {
		t.Fatalf("deliverer sends=%d want 1", dl.sends)
	}
	if countBots(t, db, "slack:T/C/U") != 1 {
		t.Fatal("want exactly one bot row")
	}
	row := turnBotRow(t, db, "slack:T/C/U", "t1")
	if st := statusOf(t, db, row.ID); st != core.MessageStatusSent || row.PlatformID != "1716.0042" {
		t.Fatalf("bot row = (%q, %q), want (sent, 1716.0042)", st, row.PlatformID)
	}
}

// TestResultRecordsOutcome checks recordTurnResult (submit_turn) records
// session_id, cost, flips turn state, and dedups on (folder, turn_id).
func TestResultRecordsOutcome(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	srv := NewServer(db, nil, nil, nil, 0, "")
	db.PutTurnContext("t9", "demo", "main", "web:demo", "u1", "")

	body := apiv1.TurnResult{TurnID: "t9", SessionID: "sX", Status: "success",
		Models: map[string]apiv1.ModelCost{"claude": {Input: 100, Output: 20, CostCents: 3}}}
	first, err := srv.recordTurnResult("t9", body)
	if err != nil || !first {
		t.Fatalf("first result: recorded=%v err=%v", first, err)
	}
	if got := db.SessionID("demo", "main"); got != "sX" {
		t.Fatalf("session not persisted: %q", got)
	}
	// duplicate → recorded:false.
	if again, err := srv.recordTurnResult("t9", body); err != nil || again {
		t.Fatalf("duplicate result: recorded=%v err=%v (want dropped)", again, err)
	}
}

// TestRouteTokenRoundTrip checks a REST-minted token resolves. Resolution is
// deliberately NOT an HTTP leg: webd and proxyd are FS-mounted on routd.db and
// call store.LookupRouteToken in-process, so that is what the round trip
// exercises (spec 5/W § Resolution).
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

	row, ok := resolveToken(db, issued.Token)
	if !ok || row.JID != "web:acme" || row.OwnerFolder != "acme" {
		t.Fatalf("resolve: jid=%q owner=%q ok=%v", row.JID, row.OwnerFolder, ok)
	}
}

// --- helpers ---

type fakeDeliverer struct {
	platformID string
	sends      int
	docErr     error
	// sendErr makes Send fail, which is the pending path: the row is stored as
	// it stands and the outbound sweep re-sends it later.
	sendErr error
}

func (d *fakeDeliverer) Send(_, _, _, _, _, _ string) (string, error) {
	d.sends++
	if d.sendErr != nil {
		return "", d.sendErr
	}
	return d.platformID, nil
}
func (d *fakeDeliverer) SendVia(_, _, _ string) (string, error) {
	d.sends++
	return d.platformID, nil
}
func (d *fakeDeliverer) SendOptions(_, _ string, _ []chanlib.Option, _, _ string) (string, error) {
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
// must SKIP the send — no retry — instead of ERROR-spamming a doomed Send that
// trips the per-folder breaker en masse on a MIGRATION_VERSION bump and blocks
// real replies for that folder. A real channel jid still delivers.
//
// The row must land `failed`, not `sent`: `sent` records a delivery that never
// happened, and it hid two operator alerts — one reporting a skills-breaking
// guard (BUGS L76). `failed` IS the dead-letter state, so the content stays
// visible on the dead_letters surface. Terminal either way; PendingOutbound
// selects status='pending' only, so nothing re-sends it.
func TestDeliverRow_SkipsBareFolderJid(t *testing.T) {
	dl := &fakeDeliverer{platformID: "pid-1"}
	srv := &Server{deliver: dl}

	row := &core.Message{ID: "out-1"}
	status := srv.deliverRow(TurnContext{Folder: "atlas"}, "atlas", row, "", "")
	if status != core.MessageStatusFailed {
		t.Fatalf("bare-folder jid: want failed (dead-lettered, not delivered), got %q", status)
	}
	if dl.sends != 0 {
		t.Fatalf("bare-folder jid must NOT call Send, got %d", dl.sends)
	}

	// A prefix-less ROOM takes the same path as a bare folder: it names a chat,
	// but with no `platform:` no adapter can own it, so ForJID has no answer and
	// there is nothing better to resolve it to. It dead-letters rather than
	// routing anywhere — the shape a stale route leaves behind.
	rowRoom := &core.Message{ID: "out-room"}
	statusRoom := srv.deliverRow(TurnContext{Folder: "atlas"}, "user/137865889", rowRoom, "", "")
	if statusRoom != core.MessageStatusFailed {
		t.Fatalf("prefix-less room: want failed (dead-lettered), got %q", statusRoom)
	}
	if dl.sends != 0 {
		t.Fatalf("prefix-less room must NOT call Send, got %d", dl.sends)
	}

	row2 := &core.Message{ID: "out-2"}
	status2 := srv.deliverRow(TurnContext{Folder: "atlas"}, "slack:T/C/1", row2, "", "")
	if dl.sends != 1 || status2 != core.MessageStatusSent {
		t.Fatalf("channel jid: want 1 Send + sent, got sends=%d status=%q", dl.sends, status2)
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

// countBots counts one chat's bot rows — the agent's own output, read back
// through the chat partition that holds it.
func countBots(t *testing.T, db *DB, jid string) int {
	t.Helper()
	msgs, err := db.MessagesSince(jid, "")
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, m := range msgs {
		if m.BotMsg {
			n++
		}
	}
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

// A reply aimed at a thread must carry that thread on the row even when the
// send FAILS. PutOutbound stores the row as it stands, and the outbound sweep
// re-sends it with threadRoot="" — so a row left with no topic came back
// TOP-LEVEL, outside the thread it was written for (BUGS L169).
func TestDeliverRow_AFailedSendStillRecordsItsThread(t *testing.T) {
	dl := &fakeDeliverer{platformID: "pid-1", sendErr: errors.New("platform said no")}
	srv := &Server{deliver: dl}

	row := &core.Message{ID: "out-1"}
	status := srv.deliverRow(TurnContext{Folder: "atlas"}, "slack:T/C/1", row, "root-9", "")

	if status != core.MessageStatusPending {
		t.Fatalf("status = %q, want pending — the sweep re-sends this row", status)
	}
	if row.Topic != "root-9" {
		t.Errorf("row.Topic = %q, want root-9; the sweep has no thread to aim at", row.Topic)
	}
}

// A send that succeeds records the same thread, and a row that already has a
// topic keeps its own — the stamp fills a gap, it does not overwrite.
func TestDeliverRow_TheThreadStampFillsOnlyAnEmptyTopic(t *testing.T) {
	dl := &fakeDeliverer{platformID: "pid-1"}
	srv := &Server{deliver: dl}

	fresh := &core.Message{ID: "out-1"}
	if st := srv.deliverRow(TurnContext{Folder: "atlas"}, "slack:T/C/1", fresh, "root-9", ""); st != core.MessageStatusSent {
		t.Fatalf("status = %q, want sent", st)
	}
	if fresh.Topic != "root-9" {
		t.Errorf("a sent reply lost its thread: %q", fresh.Topic)
	}

	owned := &core.Message{ID: "out-2", Topic: "its-own"}
	srv.deliverRow(TurnContext{Folder: "atlas"}, "slack:T/C/1", owned, "root-9", "")
	if owned.Topic != "its-own" {
		t.Errorf("the stamp overwrote an existing topic: %q", owned.Topic)
	}
}

// Agent-to-agent delegation reaches the child as a `send`, and every
// agent-authored row is stamped BotMsg — so the child's chat holds only bot
// rows, the all-bot batch is dropped, and no container ever runs. The drop is
// silent by design, which is why that went unnoticed from August (BUGS L2).
// The CROSS-FOLDER case must say so.
func TestDispatch_ADelegationThatWokeNothingSaysSo(t *testing.T) {
	db, loop, rr := recLoop(t)
	logged := captureInfoLog(t)

	// The parent wrote into the child's own chat. Sender is the PARENT folder.
	if err := db.PutMessage(core.Message{ID: "d1", ChatJID: "web:eval/child",
		Sender: "eval", Content: "Task: do the thing", BotMsg: true, FromMe: true,
		Timestamp: time.Now().UTC(), Verb: "message"}); err != nil {
		t.Fatal(err)
	}
	if _, err := loop.processGroupMessages("web:eval/child"); err != nil {
		t.Fatalf("process: %v", err)
	}

	if len(rr.runs) != 0 {
		t.Errorf("a container ran for an all-bot batch: %+v", rr.runs)
	}
	out := logged()
	if !strings.Contains(out, "delegation woke nothing") {
		t.Errorf("a cross-folder delegation was dropped silently:\n%s", out)
	}
	if !strings.Contains(out, "eval/child") {
		t.Errorf("the notice does not name the chat:\n%s", out)
	}
}

// An agent's OWN output is the ordinary all-bot batch and must stay silent, or
// every turn a folder takes would log a false alarm about itself.
func TestDispatch_AnAgentsOwnOutputIsDroppedQuietly(t *testing.T) {
	db, loop, _ := recLoop(t)
	logged := captureInfoLog(t)

	if err := db.PutMessage(core.Message{ID: "o1", ChatJID: "web:eval/child",
		Sender: "eval/child", Content: "my own reply", BotMsg: true, FromMe: true,
		Timestamp: time.Now().UTC(), Verb: "message"}); err != nil {
		t.Fatal(err)
	}
	if _, err := loop.processGroupMessages("web:eval/child"); err != nil {
		t.Fatalf("process: %v", err)
	}
	if strings.Contains(logged(), "delegation woke nothing") {
		t.Errorf("a folder's own output logged a false alarm:\n%s", logged())
	}
}

// A route-token chat's JID is ambiguous by grammar: `web:acme/tok` reads as
// folder `acme/tok` while its mint recorded `acme`. Parsing the folder out of
// the JID therefore made a folder's OWN reply into its own chat compare
// unequal, and the delegation notice fired once per reply, forever. The chat's
// folder is READ from the mint record (BUGS L2, the L4/L1 class).
func TestDispatch_ARouteTokenChatsOwnReplyIsNotADelegation(t *testing.T) {
	db, loop, _ := recLoop(t)
	const jid = "web:acme/tok"
	if err := db.store().InsertRouteToken("raw-token-acme", store.RouteToken{
		JID: jid, OwnerFolder: "acme", TargetFolder: "acme",
	}); err != nil {
		t.Fatalf("mint: %v", err)
	}
	logged := captureInfoLog(t)

	// acme's own reply into the chat its own token delivers to.
	if err := db.PutMessage(core.Message{ID: "o1", ChatJID: jid,
		Sender: "acme", Content: "my own reply", BotMsg: true, FromMe: true,
		Timestamp: time.Now().UTC(), Verb: "message"}); err != nil {
		t.Fatal(err)
	}
	if _, err := loop.processGroupMessages(jid); err != nil {
		t.Fatalf("process: %v", err)
	}
	if strings.Contains(logged(), "delegation woke nothing") {
		t.Errorf("a route-token chat's own reply logged a false alarm:\n%s", logged())
	}
}
