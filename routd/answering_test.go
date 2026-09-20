package routd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	apiv1 "github.com/kronael/arizuko/routd/api/v1"
	"github.com/kronael/arizuko/store"
)

func getAnswering(t *testing.T, h http.Handler, query string) (int, apiv1.AnsweringResponse) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/v1/routing/answering"+query, nil))
	var out apiv1.AnsweringResponse
	if rec.Code == 200 {
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode: %v", err)
		}
	}
	return rec.Code, out
}

// waitingFor finds one chat's row, so a test names the chat it asserts about
// rather than an index into a sorted list.
func waitingFor(out apiv1.AnsweringResponse, jid string) (apiv1.WaitingChat, bool) {
	for _, c := range out.Waiting {
		if c.ChatJID == jid {
			return c, true
		}
	}
	return apiv1.WaitingChat{}, false
}

// TestAnswering_VerdictSeparatesDeafFromBroken is the question this surface
// exists to settle, and it is the krons/rhias case in miniature.
//
// Two chats are waiting by the same measure — a human message with no reply
// after it — and they need OPPOSITE responses. `deaf` carries a verb=mention
// rule and a plain message, so no turn was ever due and the operator configured
// that; `broken` carries a plain route, so a turn was due and nothing answered.
// A surface that reported both as "waiting" would be the hand-written query all
// over again.
func TestAnswering_VerdictSeparatesDeafFromBroken(t *testing.T) {
	db, srv, _ := newTestRoutd(t)
	h := srv.Handler()
	for _, f := range []string{"rhias/nemo", "demo"} {
		if err := db.PutGroup(core.Group{Folder: f}); err != nil {
			t.Fatal(err)
		}
	}
	doJSON(t, h, "PUT", "/v1/routes", "", map[string]any{"routes": []apiv1.Route{
		{Match: "chat_jid=telegram:group/deaf verb=mention", Target: "rhias/nemo"},
		{Match: "chat_jid=telegram:group/broken", Target: "demo"},
	}})
	now := time.Now().UTC()
	put := func(m core.Message) {
		t.Helper()
		if err := db.PutMessage(m); err != nil {
			t.Fatal(err)
		}
	}
	put(core.Message{ID: "d1", ChatJID: "telegram:group/deaf", Sender: "telegram:user/7",
		ChatName: "Deaf", Content: "anyone?", Timestamp: now.Add(-time.Hour), Verb: "message"})
	put(core.Message{ID: "b1", ChatJID: "telegram:group/broken", Sender: "telegram:user/8",
		ChatName: "Broken", Content: "hello?", Timestamp: now.Add(-time.Hour), Verb: "message"})

	code, out := getAnswering(t, h, "")
	if code != 200 {
		t.Fatalf("status = %d", code)
	}
	deaf, ok := waitingFor(out, "telegram:group/deaf")
	if !ok {
		t.Fatalf("the deaf chat is not listed: %+v", out.Waiting)
	}
	// Held, and NOT attributed to the folder the mention rule names: no turn
	// would run, so no folder owes this chat an answer today.
	if deaf.Verdict != apiv1.VerdictHeld || deaf.Folder != "" {
		t.Errorf("deaf chat = verdict %q folder %q, want held and no folder",
			deaf.Verdict, deaf.Folder)
	}
	broken, ok := waitingFor(out, "telegram:group/broken")
	if !ok {
		t.Fatalf("the routed chat is not listed: %+v", out.Waiting)
	}
	if broken.Verdict != apiv1.VerdictRoute || broken.Folder != "demo" {
		t.Errorf("routed chat = verdict %q folder %q, want route into demo",
			broken.Verdict, broken.Folder)
	}
	// Never answered at all is a different fact from answered-then-stopped.
	if broken.LastReplyAt != "" {
		t.Errorf("last_reply_at = %q, want empty on a chat with no reply ever",
			broken.LastReplyAt)
	}
	if broken.Platform != "telegram" || broken.ChatName != "Broken" ||
		broken.LastSender != "telegram:user/8" {
		t.Errorf("row = %+v", broken)
	}
	if broken.WaitingSec < 3000 {
		t.Errorf("waiting_seconds = %d, want about an hour", broken.WaitingSec)
	}
}

// TestAnswering_ObserveIsAVerdictNotAFault — an #observe route consumes the
// message by the operator's own configuration. The chat is still waiting and is
// still listed, but it is named `observe` and attributed to the folder that
// watches it, because "stop observing" and "fix the agent" are different acts.
func TestAnswering_ObserveIsAVerdictNotAFault(t *testing.T) {
	db, srv, _ := newTestRoutd(t)
	h := srv.Handler()
	if err := db.PutGroup(core.Group{Folder: "staging"}); err != nil {
		t.Fatal(err)
	}
	doJSON(t, h, "PUT", "/v1/routes", "", map[string]any{"routes": []apiv1.Route{
		{Match: "platform=telegram", Target: "staging#observe"},
	}})
	if err := db.PutMessage(core.Message{ID: "o1", ChatJID: "telegram:user/3",
		Sender: "telegram:user/3", Content: "hi", Timestamp: time.Now().UTC(),
		Verb: "message"}); err != nil {
		t.Fatal(err)
	}
	_, out := getAnswering(t, h, "")
	row, ok := waitingFor(out, "telegram:user/3")
	if !ok {
		t.Fatalf("observed chat is not listed: %+v", out.Waiting)
	}
	if row.Verdict != apiv1.VerdictObserve || row.Folder != "staging" {
		t.Errorf("row = verdict %q folder %q, want observe by staging", row.Verdict, row.Folder)
	}
}

// TestAnswering_AnAnswerAfterTheQuestionClearsTheChat pins the OUTCOME test
// itself from both sides, on one chat, with only the reply's instant moving.
// This is the half the held set cannot see: the route works throughout.
func TestAnswering_AnAnswerAfterTheQuestionClearsTheChat(t *testing.T) {
	db, srv, _ := newTestRoutd(t)
	h := srv.Handler()
	if err := db.PutGroup(core.Group{Folder: "demo"}); err != nil {
		t.Fatal(err)
	}
	doJSON(t, h, "PUT", "/v1/routes", "", map[string]any{"routes": []apiv1.Route{
		{Match: "chat_jid=telegram:user/1", Target: "demo"},
	}})
	now := time.Now().UTC()
	// Written in chronological order, because the message log is append-only per
	// chat: store.appendInstant places a row that does not sort after the chat's
	// newest at newest+1ns, so a fixture that wrote the reply last would be
	// testing a chat where the reply is the NEWER row.
	if err := db.PutMessage(core.Message{ID: "stale", ChatJID: "telegram:user/1",
		Sender: "bot", Content: "earlier", Timestamp: now.Add(-2 * time.Hour),
		BotMsg: true}); err != nil {
		t.Fatal(err)
	}
	// The question arrives after that reply, so the agent answered something,
	// but not this.
	if err := db.PutMessage(core.Message{ID: "q1", ChatJID: "telegram:user/1",
		Sender: "telegram:user/1", Content: "ping", Timestamp: now.Add(-time.Hour),
		Verb: "message"}); err != nil {
		t.Fatal(err)
	}
	_, out := getAnswering(t, h, "")
	row, ok := waitingFor(out, "telegram:user/1")
	if !ok {
		t.Fatalf("a chat answered only BEFORE the question is not waiting: %+v", out.Waiting)
	}
	if row.LastReplyAt == "" {
		t.Error("last_reply_at is empty, but this chat has an older reply")
	}

	// The same chat, once an answer lands after the question.
	if err := db.PutMessage(core.Message{ID: "a1", ChatJID: "telegram:user/1",
		Sender: "bot", Content: "pong", Timestamp: now.Add(-time.Minute),
		BotMsg: true}); err != nil {
		t.Fatal(err)
	}
	if _, out := getAnswering(t, h, ""); len(out.Waiting) != 0 {
		t.Errorf("answered chat still listed: %+v", out.Waiting)
	}
}

// TestAnswering_SyntheticSendersAreNotPeopleWaiting — arizuko's own rows are
// not a person waiting for an answer. A migration storm queues one `system`
// prompt per folder (55 on krons), and reporting those as people waiting is a
// fleet-wide false alarm on exactly the surface an operator turns to.
func TestAnswering_SyntheticSendersAreNotPeopleWaiting(t *testing.T) {
	db, srv, _ := newTestRoutd(t)
	h := srv.Handler()
	if err := db.PutGroup(core.Group{Folder: "demo"}); err != nil {
		t.Fatal(err)
	}
	doJSON(t, h, "PUT", "/v1/routes", "", map[string]any{"routes": []apiv1.Route{
		{Match: "platform=telegram", Target: "demo"},
	}})
	now := time.Now().UTC()
	for i, sender := range store.SyntheticSenders {
		if err := db.PutMessage(core.Message{
			ID: core.MsgID(sender), ChatJID: "telegram:user/" + sender,
			Sender: sender, Content: "auto", Timestamp: now.Add(-time.Duration(i) * time.Minute),
			Verb: "message",
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, out := getAnswering(t, h, ""); len(out.Waiting) != 0 {
		t.Errorf("arizuko's own rows reported as people waiting: %+v", out.Waiting)
	}

	// A person in the same chat IS waiting, so the filter is on the sender and
	// not on the chat.
	if err := db.PutMessage(core.Message{ID: "p1",
		ChatJID: "telegram:user/" + store.SystemSender, Sender: "telegram:user/9",
		Content: "and me?", Timestamp: now, Verb: "message"}); err != nil {
		t.Fatal(err)
	}
	_, out := getAnswering(t, h, "")
	if _, ok := waitingFor(out, "telegram:user/"+store.SystemSender); !ok {
		t.Errorf("a person in a chat that also carries synthetic rows is not listed: %+v",
			out.Waiting)
	}
}

// TestAnswering_TurnOutcomesCountTheWindow — the dispatcher half. Expired is
// counted apart from done, because "turns are expiring" is the fact the
// maintainer could not read off any surface.
func TestAnswering_TurnOutcomesCountTheWindow(t *testing.T) {
	db, srv, _ := newTestRoutd(t)
	h := srv.Handler()
	open := func(id, folder, state string) {
		t.Helper()
		if _, err := db.PutTurnContext(id, folder, "", "telegram:user/1", "u", ""); err != nil {
			t.Fatal(err)
		}
		if state != store.TurnRunning {
			if err := db.SetTurnState(id, state); err != nil {
				t.Fatal(err)
			}
		}
	}
	open("t1", "demo", store.TurnDone)
	open("t2", "demo", store.TurnDone)
	open("t3", "demo", store.TurnExpired)
	open("t4", "demo", store.TurnRunning)
	open("t5", "demo", store.TurnPendingRetry)
	// A state this surface does not name still has to be counted, or the
	// numbers stop summing to the turns in the window.
	open("t6", "demo", "wedged")

	_, out := getAnswering(t, h, "")
	want := apiv1.TurnOutcomes{Done: 2, Expired: 1, Running: 1, PendingRetry: 1, Other: 1}
	if out.Turns != want {
		t.Errorf("turns = %+v, want %+v", out.Turns, want)
	}

	// The window bounds the count: nothing was dispatched after now.
	since := time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano)
	_, out = getAnswering(t, h, "?since="+since)
	if (out.Turns != apiv1.TurnOutcomes{}) {
		t.Errorf("turns outside the window = %+v, want all zero", out.Turns)
	}
	if out.Since != since {
		t.Errorf("since = %q, want the window echoed back as %q", out.Since, since)
	}
}

// TestAnswering_ScopedTokenSeesItsSubtreeAndNoHeldChats is the containment.
//
// An unclaimed chat's folder is EMPTY, and ownsFolder counts an empty target as
// owned by anyone — so a folder-scoped bearer reaching this read through
// ownsFolder would be handed every unclaimed chat on the instance. The test
// pins descendant() semantics from all three sides: own subtree visible, a
// sibling folder invisible, held rows invisible.
func TestAnswering_ScopedTokenSeesItsSubtreeAndNoHeldChats(t *testing.T) {
	db, srv, _ := newTestRoutd(t)
	h := srv.Handler()
	for _, f := range []string{"corp/eng", "corp/sales"} {
		if err := db.PutGroup(core.Group{Folder: f}); err != nil {
			t.Fatal(err)
		}
	}
	doJSON(t, h, "PUT", "/v1/routes", "", map[string]any{"routes": []apiv1.Route{
		{Match: "chat_jid=telegram:group/eng", Target: "corp/eng"},
		{Match: "chat_jid=telegram:group/sales", Target: "corp/sales"},
	}})
	now := time.Now().UTC()
	for _, jid := range []string{"telegram:group/eng", "telegram:group/sales", "telegram:group/held"} {
		if err := db.PutMessage(core.Message{ID: core.MsgID(jid), ChatJID: jid,
			Sender: "telegram:user/1", Content: "hi", Timestamp: now,
			Verb: "message"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.PutTurnContext("eng-turn", "corp/eng", "", "telegram:group/eng", "u", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := db.PutTurnContext("sales-turn", "corp/sales", "", "telegram:group/sales", "u", ""); err != nil {
		t.Fatal(err)
	}

	srv.verify = fakeVerifier{sub: "agent", scope: []string{"routes:read:own_group"}, folder: "corp/eng"}
	code, out := getAnswering(t, srv.Handler(), "")
	if code != 200 {
		t.Fatalf("scoped token = %d, want 200", code)
	}
	if len(out.Waiting) != 1 || out.Waiting[0].ChatJID != "telegram:group/eng" {
		t.Errorf("scoped token sees %+v, want only its own chat", out.Waiting)
	}
	if out.Turns.Running != 1 {
		t.Errorf("scoped token counts %d running turns, want only its own", out.Turns.Running)
	}

	// The root/service bearer sees the whole instance, held chat included.
	srv.verify = fakeVerifier{sub: "service:dashd", scope: []string{"routes:read"}}
	_, out = getAnswering(t, srv.Handler(), "")
	if len(out.Waiting) != 3 {
		t.Errorf("service token sees %d chats, want all 3", len(out.Waiting))
	}
	if out.Turns.Running != 2 {
		t.Errorf("service token counts %d running turns, want 2", out.Turns.Running)
	}
}

// TestAnswering_GateAndWindowRefusals — an untested gate is no gate, and a
// window nobody chose would report the wrong period under a deliberate-looking
// timestamp.
func TestAnswering_GateAndWindowRefusals(t *testing.T) {
	_, srv, _ := newTestRoutd(t)
	srv.verify = fakeVerifier{sub: "user:nobody", scope: []string{"audit:read"}}
	if code, _ := getAnswering(t, srv.Handler(), ""); code != 403 {
		t.Errorf("token without routes:read = %d, want 403", code)
	}
	srv.verify = fakeVerifier{sub: "service:dashd", scope: []string{"routes:read"}}
	if code, _ := getAnswering(t, srv.Handler(), "?since=last-tuesday"); code != 400 {
		t.Errorf("unparseable since = %d, want 400", code)
	}

	// Without a loop there is no resolve, so there is no verdict — and a
	// waiting list with no verdicts is the ambiguity this read removes.
	_, _, h := dashSrv(t)
	if code, _ := getAnswering(t, h, ""); code != 503 {
		t.Errorf("no loop = %d, want 503", code)
	}
}
