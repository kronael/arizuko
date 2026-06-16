package routd

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	apiv1 "github.com/kronael/arizuko/routd/api/v1"
	runedv1 "github.com/kronael/arizuko/runed/api/v1"
)

// turnRec is one dispatched run captured by recRunner.
type turnRec struct {
	turnID, folder, topic, batch, trigger, model string
}

// recRunner records every POST /v1/runs and returns a clean ok outcome. Used
// by the parity tests to assert per-topic / per-sender turn fan-out without
// the callback round-trip stubRunner does.
type recRunner struct{ runs []turnRec }

func (r *recRunner) Run(_ context.Context, req runedv1.RunRequest) (runedv1.RunOutcome, error) {
	r.runs = append(r.runs, turnRec{
		turnID: req.TurnID, folder: string(req.Folder), topic: req.Topic,
		batch: req.MessageBatch, trigger: req.TriggerSender, model: req.Model,
	})
	return runedv1.RunOutcome{RunID: "r", Outcome: runedv1.OutcomeOK, SessionID: "s"}, nil
}

func recLoop(t *testing.T) (*DB, *Loop, *recRunner) {
	t.Helper()
	db, err := OpenMem()
	if err != nil {
		t.Fatalf("open mem: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	rr := &recRunner{}
	loop := NewLoop(db, rr, LoopConfig{})
	loop.StopQueue()
	return db, loop, rr
}

// TestDefaultModelFallback: a group with no per-group model dispatches the
// instance-wide default (Loop.defaultModel); a per-group model still wins.
func TestDefaultModelFallback(t *testing.T) {
	db, loop, rr := recLoop(t)
	loop.defaultModel = "claude-opus-4-8"
	_ = db.PutGroup(core.Group{Folder: "nomodel"})
	_ = db.PutGroup(core.Group{Folder: "pinned", Model: "claude-sonnet-4-5"})
	now := time.Now().UTC()
	_ = db.PutMessage(core.Message{ID: "a", ChatJID: "web:nomodel", Sender: "u",
		Content: "q", Timestamp: now})
	_ = db.PutMessage(core.Message{ID: "b", ChatJID: "web:pinned", Sender: "u",
		Content: "q", Timestamp: now})

	if _, err := loop.processGroupMessages("web:nomodel"); err != nil {
		t.Fatalf("process nomodel: %v", err)
	}
	if _, err := loop.processGroupMessages("web:pinned"); err != nil {
		t.Fatalf("process pinned: %v", err)
	}
	got := map[string]string{}
	for _, r := range rr.runs {
		got[r.folder] = r.model
	}
	if got["nomodel"] != "claude-opus-4-8" {
		t.Fatalf("empty group model: spec.Model=%q want claude-opus-4-8 (default)", got["nomodel"])
	}
	if got["pinned"] != "claude-sonnet-4-5" {
		t.Fatalf("per-group model: spec.Model=%q want claude-sonnet-4-5 (override)", got["pinned"])
	}
}

// TestTurnContextRunIDPersisted: a dispatched turn records the runed-assigned
// run_id (RunOutcome.RunID) into turn_context.run_id — the reconciliation handle
// that was previously left NULL.
func TestTurnContextRunIDPersisted(t *testing.T) {
	db, loop, _ := recLoop(t)
	_ = db.PutGroup(core.Group{Folder: "demo"})
	_ = db.PutMessage(core.Message{ID: "a", ChatJID: "web:demo", Sender: "u",
		Content: "q", Timestamp: time.Now().UTC()})

	if _, err := loop.processGroupMessages("web:demo"); err != nil {
		t.Fatalf("process: %v", err)
	}
	var runID string
	if err := db.SQL().QueryRow("SELECT run_id FROM turn_context WHERE turn_id='a'").Scan(&runID); err != nil {
		t.Fatalf("read turn_context.run_id: %v", err)
	}
	if runID != "r" {
		t.Fatalf("turn_context.run_id=%q want r (recRunner's RunID)", runID)
	}
}

// TestWebPerTopicDispatch: a web: chat batches adjacent same-topic rows into one
// turn and forks a new turn when the topic changes, in first-seen order (gated
// processWebTopics consecutive-run parity). Interleaved topics are covered by
// TestGroupByTopicConsecutiveRuns.
func TestWebPerTopicDispatch(t *testing.T) {
	db, loop, rr := recLoop(t)
	_ = db.PutGroup(core.Group{Folder: "demo"})
	now := time.Now().UTC()
	_ = db.PutMessage(core.Message{ID: "a", ChatJID: "web:demo", Sender: "u", Content: "q1", Topic: "alpha", Timestamp: now})
	_ = db.PutMessage(core.Message{ID: "b", ChatJID: "web:demo", Sender: "u", Content: "q3", Topic: "alpha", Timestamp: now.Add(time.Second)})
	_ = db.PutMessage(core.Message{ID: "c", ChatJID: "web:demo", Sender: "u", Content: "q2", Topic: "beta", Timestamp: now.Add(2 * time.Second)})

	if _, err := loop.processGroupMessages("web:demo"); err != nil {
		t.Fatalf("process: %v", err)
	}
	if len(rr.runs) != 2 {
		t.Fatalf("turns=%d want 2 (one per topic): %+v", len(rr.runs), rr.runs)
	}
	if rr.runs[0].topic != "alpha" || rr.runs[1].topic != "beta" {
		t.Fatalf("topic order=%q,%q want alpha,beta", rr.runs[0].topic, rr.runs[1].topic)
	}
	// the alpha turn batches both adjacent alpha rows (q1 + q3).
	if !contains(rr.runs[0].batch, "q1") || !contains(rr.runs[0].batch, "q3") {
		t.Fatalf("alpha batch missing q1/q3: %q", rr.runs[0].batch)
	}
	if db.GetAgentCursor("web:demo") == "" {
		t.Fatal("cursor not advanced after all topic turns")
	}
}

// TestGroupBySenderBatching: a multi-party chat batches adjacent same-sender
// rows into one turn and forks a new turn when the sender changes, in first-seen
// order (gated processSenderBatch consecutive-run parity). Interleaved senders
// are covered by TestGroupBySenderConsecutiveRuns.
func TestGroupBySenderBatching(t *testing.T) {
	db, loop, rr := recLoop(t)
	_ = db.PutGroup(core.Group{Folder: "demo"})
	doSetRoutes(t, db, []core.Route{{Match: "platform=slack", Target: "demo"}})
	now := time.Now().UTC()
	_ = db.PutMessage(core.Message{ID: "a", ChatJID: "slack:T/C/X", Sender: "alice", Content: "m1", Timestamp: now, Verb: "message"})
	_ = db.PutMessage(core.Message{ID: "b", ChatJID: "slack:T/C/X", Sender: "alice", Content: "m3", Timestamp: now.Add(time.Second), Verb: "message"})
	_ = db.PutMessage(core.Message{ID: "c", ChatJID: "slack:T/C/X", Sender: "bob", Content: "m2", Timestamp: now.Add(2 * time.Second), Verb: "message"})

	if _, err := loop.processGroupMessages("slack:T/C/X"); err != nil {
		t.Fatalf("process: %v", err)
	}
	if len(rr.runs) != 2 {
		t.Fatalf("turns=%d want 2 (one per sender): %+v", len(rr.runs), rr.runs)
	}
	if rr.runs[0].trigger != "alice" || rr.runs[1].trigger != "bob" {
		t.Fatalf("sender order=%q,%q want alice,bob", rr.runs[0].trigger, rr.runs[1].trigger)
	}
	if !contains(rr.runs[0].batch, "m1") || !contains(rr.runs[0].batch, "m3") {
		t.Fatalf("alice batch missing m1/m3: %q", rr.runs[0].batch)
	}
}

// TestStickyGroupNav: a bare @<folder> pins routing and is consumed (no turn);
// a subsequent bare message routes to the pinned group (gated handleStickyCommand).
func TestStickyNavConsumed(t *testing.T) {
	db, loop, rr := recLoop(t)
	dl := &recDeliverer{}
	loop.deliver = dl
	_ = db.PutGroup(core.Group{Folder: "demo"})
	_ = db.PutGroup(core.Group{Folder: "child"})
	// the chat routes to demo via the route table; sticky-nav runs post-resolve
	// (mirrors gated: handleStickyCommand fires only after resolveOrEngaged).
	doSetRoutes(t, db, []core.Route{{Match: "platform=tg", Target: "demo"}})
	now := time.Now().UTC()
	_ = db.PutMessage(core.Message{ID: "a", ChatJID: "tg:1", Sender: "u", Content: "@child", Timestamp: now, Verb: "message"})

	loop.pollOnce()
	if g, _ := db.StickyState("tg:1"); g != "child" {
		t.Fatalf("sticky_group=%q want child", g)
	}
	if len(rr.runs) != 0 {
		t.Fatalf("sticky command dispatched a turn: %+v", rr.runs)
	}
	if db.GetAgentCursor("tg:1") == "" {
		t.Fatal("consumed sticky command did not advance cursor")
	}
	if len(dl.sends) != 1 {
		t.Fatalf("sticky ack sends=%d want 1", len(dl.sends))
	}
}

// TestSlashNewClearsSession: /new clears the resolved folder's session and is
// consumed (no turn). /chatid acks the jid (gated handleCommand subset).
func TestSlashNewClearsSession(t *testing.T) {
	db, loop, _ := recLoop(t)
	dl := &recDeliverer{}
	loop.deliver = dl
	_ = db.PutGroup(core.Group{Folder: "demo"})
	_ = db.PutSession("demo", "", "sess-X")
	_ = db.PutMessage(core.Message{ID: "a", ChatJID: "web:demo", Sender: "u", Content: "/new", Timestamp: time.Now().UTC()})

	loop.pollOnce()
	if db.SessionID("demo", "") != "" {
		t.Fatal("/new did not clear session")
	}
	if db.GetAgentCursor("web:demo") == "" {
		t.Fatal("/new did not advance cursor")
	}
}

// TestChildDelegation: an @child prefix delegates to folder/child, appending a
// delegation row carrying the origin chat as forwarded_from (the return
// address). Mirrors gated handlePrefixLayer + delegateViaMessage.
func TestChildDelegation(t *testing.T) {
	db, loop, _ := recLoop(t)
	_ = db.PutGroup(core.Group{Folder: "root"})
	_ = db.PutGroup(core.Group{Folder: "root/eng"})
	doSetRoutes(t, db, []core.Route{{Match: "platform=slack", Target: "root"}})
	_ = db.PutMessage(core.Message{ID: "a", ChatJID: "slack:T/C/X", Sender: "u",
		Content: "@eng please ship it", Timestamp: time.Now().UTC(), Verb: "message"})

	loop.pollOnce()
	// the delegation row landed on the child folder JID with the return address.
	msgs, _ := db.MessagesSince("root/eng", "")
	if len(msgs) != 1 {
		t.Fatalf("delegation rows on child=%d want 1", len(msgs))
	}
	if msgs[0].ForwardedFrom != "slack:T/C/X" {
		t.Fatalf("forwarded_from=%q want slack:T/C/X", msgs[0].ForwardedFrom)
	}
	if msgs[0].Content != "please ship it" {
		t.Fatalf("delegated content=%q want stripped prompt", msgs[0].Content)
	}
	if db.GetAgentCursor("slack:T/C/X") == "" {
		t.Fatal("consumed delegation did not advance origin cursor")
	}
}

// TestObserveMarksMessages: a route in observe mode (target=folder#observe)
// flags inbound rows is_observed=1 and routed_to=folder, fires no turn, and
// advances the cursor (gated MarkMessagesObserved parity).
func TestObserveMarksMessages(t *testing.T) {
	db, loop, rr := recLoop(t)
	_ = db.PutGroup(core.Group{Folder: "demo"})
	doSetRoutes(t, db, []core.Route{{Match: "platform=slack", Target: "demo#observe"}})
	_ = db.PutMessage(core.Message{ID: "a", ChatJID: "slack:T/C/X", Sender: "u",
		Content: "ambient", Timestamp: time.Now().UTC(), Verb: "message"})

	had, err := loop.processGroupMessages("slack:T/C/X")
	if err != nil {
		t.Fatalf("process: %v", err)
	}
	if had || len(rr.runs) != 0 {
		t.Fatalf("observe-mode fired a turn (had=%v runs=%d)", had, len(rr.runs))
	}
	var obs int
	var routed string
	db.SQL().QueryRow("SELECT is_observed, routed_to FROM messages WHERE id='a'").Scan(&obs, &routed)
	if obs != 1 || routed != "demo" {
		t.Fatalf("observe row not marked: is_observed=%d routed_to=%q", obs, routed)
	}
	if db.GetAgentCursor("slack:T/C/X") == "" {
		t.Fatal("observe ingest did not advance cursor")
	}
}

// TestEngagementTopicRootNormalization: engagement recorded on the root topic
// ("") also governs a thread message (topic="<thread>") that has no engagement
// record of its own — the thread→root fallback (gated poll engTopic).
func TestEngagementTopicRootNormalization(t *testing.T) {
	db, loop, _ := recLoop(t)
	_ = db.PutGroup(core.Group{Folder: "eng"})
	// engagement claimed on root topic for the chat → eng.
	_ = db.SetEngagement("slack:T/C/X", "", "eng", time.Hour)
	// a message arrives in a thread (its own topic, no engagement record).
	last := core.Message{ID: "a", ChatJID: "slack:T/C/X", Sender: "u",
		Content: "follow-up", Topic: "1700.0001", Timestamp: time.Now().UTC(), Verb: "message"}

	folder, ok := loop.resolveGroup("slack:T/C/X", last)
	if !ok || folder != "eng" {
		t.Fatalf("resolve=%q,%v want eng,true (thread→root engagement)", folder, ok)
	}
}

// TestRouteGetScopeFilter: a folder-scoped token cannot GET a route whose
// target lies outside its subtree — 404, not a leak (info-leak fix).
func TestRouteGetScopeFilter(t *testing.T) {
	db, h := authSrv(t, fakeVerifier{sub: "user:a", scope: []string{"routes:read:own_group"}, folder: "a"})
	id, err := db.AddRoute(core.Route{Seq: 1, Match: "platform=slack", Target: "b"})
	if err != nil {
		t.Fatal(err)
	}
	rec := doJSON(t, h, "GET", "/v1/routes/"+strconv.FormatInt(id, 10), "", nil)
	if rec.Code != 404 {
		t.Fatalf("scoped GET of foreign route = %d want 404 body=%s", rec.Code, rec.Body.String())
	}
}

// TestNoEngagementOnMentionIngress: a verb=mention ingress no longer commits an
// empty-folder engagement row (which would make Engaged return ("",true) and
// misroute). The chat resolves via the route table, not a phantom engagement.
func TestNoEngagementOnMentionIngress(t *testing.T) {
	db, srv, _ := newTestRoutd(t)
	h := srv.Handler()
	_ = db.PutGroup(core.Group{Folder: "demo"})
	doJSON(t, h, "PUT", "/v1/routes", "", []apiv1.Route{{Match: "platform=slack", Target: "demo"}})
	in := apiv1.Message{ID: "m1", ChatJID: "slack:T/C/U", Sender: "u1", Content: "hi", Verb: "mention"}
	if rec := doJSON(t, h, "POST", "/v1/messages", "", in); rec.Code != 200 {
		t.Fatalf("ingest=%d", rec.Code)
	}
	// no engagement record was written at ingress.
	if _, ok := db.Engaged("slack:T/C/U", ""); ok {
		t.Fatal("ingress committed an engagement (want deferred to dispatch)")
	}
	// the message still resolves via the route table (not a phantom engagement).
	last := core.Message{ChatJID: "slack:T/C/U", Verb: "mention"}
	if folder, ok := srv.loop.resolveGroup("slack:T/C/U", last); !ok || folder != "demo" {
		t.Fatalf("resolve=%q,%v want demo,true", folder, ok)
	}
}

// TestThreadParticipationPromotion: a reply landing in a thread the bot has
// already spoken in is promoted to verb=mention even when the reply target is a
// human message — so the agent re-attends a thread it joined instead of going
// silent once its 5/G window closes (spec 5/L). A thread the bot never spoke in
// is not promoted; a no-topic reply to a topic-less message is not promoted.
func TestThreadParticipationPromotion(t *testing.T) {
	db, srv, _ := newTestRoutd(t)
	h := srv.Handler()
	_ = db.PutGroup(core.Group{Folder: "demo"})
	doJSON(t, h, "PUT", "/v1/routes", "", []apiv1.Route{{Match: "platform=slack", Target: "demo"}})
	now := time.Now().UTC()
	// Bot participated in thread "th1" of chat C1; the thread root is a HUMAN msg
	// (so promotion can only come from participation, not reply-to-bot). "dm-human"
	// is a topic-less message in the same chat.
	_ = db.PutMessage(core.Message{ID: "bot-1", ChatJID: "slack:T/C1/U", Sender: "bot",
		Content: "answer", Timestamp: now, BotMsg: true, Topic: "th1", RoutedTo: "demo"})
	_ = db.PutMessage(core.Message{ID: "human-root", ChatJID: "slack:T/C1/U", Sender: "u1",
		Content: "question", Timestamp: now, Topic: "th1"})
	_ = db.PutMessage(core.Message{ID: "dm-human", ChatJID: "slack:T/C1/U", Sender: "u1",
		Content: "ping", Timestamp: now})

	storedVerb := func(id string) string {
		var v string
		db.db.QueryRow("SELECT verb FROM messages WHERE id=?", id).Scan(&v)
		return v
	}

	cases := []struct {
		name, id, chat, topic, replyTo, want string
	}{
		{"participated-thread", "m1", "slack:T/C1/U", "th1", "human-root", "mention"},
		{"silent-thread", "m2", "slack:T/C2/U", "untouched", "human-root", "message"},
		{"no-topic-reply", "m3", "slack:T/C1/U", "", "dm-human", "message"},
	}
	for _, c := range cases {
		in := apiv1.Message{ID: c.id, ChatJID: c.chat, Sender: "u1", Content: "x",
			Verb: "message", ReplyTo: c.replyTo, Topic: c.topic}
		if rec := doJSON(t, h, "POST", "/v1/messages", "", in); rec.Code != 200 {
			t.Fatalf("%s: ingest=%d", c.name, rec.Code)
		}
		if v := storedVerb(c.id); v != c.want {
			t.Errorf("%s: verb=%q want %q", c.name, v, c.want)
		}
	}
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }

// TestGroupBySenderConsecutiveRuns: an interleaved A,B,A backlog splits into
// THREE ordered batches [A],[B],[A], not the reordered [A,A],[B] a whole-slice
// map regroup would produce (gated groupBySender consecutive-run parity).
func TestGroupBySenderConsecutiveRuns(t *testing.T) {
	db, loop, rr := recLoop(t)
	_ = db.PutGroup(core.Group{Folder: "demo"})
	doSetRoutes(t, db, []core.Route{{Match: "platform=slack", Target: "demo"}})
	now := time.Now().UTC()
	_ = db.PutMessage(core.Message{ID: "a", ChatJID: "slack:T/C/X", Sender: "alice", Content: "m1", Timestamp: now, Verb: "message"})
	_ = db.PutMessage(core.Message{ID: "b", ChatJID: "slack:T/C/X", Sender: "bob", Content: "m2", Timestamp: now.Add(time.Second), Verb: "message"})
	_ = db.PutMessage(core.Message{ID: "c", ChatJID: "slack:T/C/X", Sender: "alice", Content: "m3", Timestamp: now.Add(2 * time.Second), Verb: "message"})

	if _, err := loop.processGroupMessages("slack:T/C/X"); err != nil {
		t.Fatalf("process: %v", err)
	}
	if len(rr.runs) != 3 {
		t.Fatalf("turns=%d want 3 (A,B,A consecutive runs): %+v", len(rr.runs), rr.runs)
	}
	got := []string{rr.runs[0].trigger, rr.runs[1].trigger, rr.runs[2].trigger}
	if got[0] != "alice" || got[1] != "bob" || got[2] != "alice" {
		t.Fatalf("sender order=%v want [alice bob alice]", got)
	}
	if !contains(rr.runs[0].batch, "m1") || !contains(rr.runs[2].batch, "m3") {
		t.Fatalf("batches lost causal order: %q / %q", rr.runs[0].batch, rr.runs[2].batch)
	}
}

// TestGroupByTopicConsecutiveRuns: an interleaved alpha,beta,alpha web backlog
// splits into THREE ordered batches, not the reordered [alpha,alpha],[beta] a
// whole-slice map regroup would produce (gated processWebTopics parity).
func TestGroupByTopicConsecutiveRuns(t *testing.T) {
	db, loop, rr := recLoop(t)
	_ = db.PutGroup(core.Group{Folder: "demo"})
	now := time.Now().UTC()
	_ = db.PutMessage(core.Message{ID: "a", ChatJID: "web:demo", Sender: "u", Content: "q1", Topic: "alpha", Timestamp: now})
	_ = db.PutMessage(core.Message{ID: "b", ChatJID: "web:demo", Sender: "u", Content: "q2", Topic: "beta", Timestamp: now.Add(time.Second)})
	_ = db.PutMessage(core.Message{ID: "c", ChatJID: "web:demo", Sender: "u", Content: "q3", Topic: "alpha", Timestamp: now.Add(2 * time.Second)})

	if _, err := loop.processGroupMessages("web:demo"); err != nil {
		t.Fatalf("process: %v", err)
	}
	if len(rr.runs) != 3 {
		t.Fatalf("turns=%d want 3 (alpha,beta,alpha consecutive runs): %+v", len(rr.runs), rr.runs)
	}
	got := []string{rr.runs[0].topic, rr.runs[1].topic, rr.runs[2].topic}
	if got[0] != "alpha" || got[1] != "beta" || got[2] != "alpha" {
		t.Fatalf("topic order=%v want [alpha beta alpha]", got)
	}
	if !contains(rr.runs[0].batch, "q1") || !contains(rr.runs[2].batch, "q3") {
		t.Fatalf("batches lost causal order: %q / %q", rr.runs[0].batch, rr.runs[2].batch)
	}
}

// TestSlashNewReinjectsFollowup: `/new look into X` clears the resolved
// folder's session AND reinjects "look into X" as a fresh inbound that runs on
// the cleared session (gated cmdNew followup parity). A bare /new only clears.
func TestSlashNewReinjectsFollowup(t *testing.T) {
	db, loop, rr := recLoop(t)
	dl := &recDeliverer{}
	loop.deliver = dl
	_ = db.PutGroup(core.Group{Folder: "demo"})
	_ = db.PutSession("demo", "", "sess-X")
	_ = db.PutMessage(core.Message{ID: "a", ChatJID: "web:demo", Sender: "u",
		Content: "/new look into X", Timestamp: time.Now().UTC()})

	loop.pollOnce() // consumes /new: clears session, reinjects followup, advances past /new
	if db.SessionID("demo", "") != "" {
		t.Fatal("/new did not clear session")
	}
	// the synthetic followup landed as a fresh inbound.
	msgs, _ := db.MessagesSince("web:demo", db.GetAgentCursor("web:demo"))
	if len(msgs) != 1 || msgs[0].Content != "look into X" {
		t.Fatalf("followup not reinjected: %+v", msgs)
	}
	// the ack mentions processing (followup non-empty path).
	if len(dl.sends) != 1 || !contains(dl.sends[0].text, "Processing your message") {
		t.Fatalf("ack=%+v want a 'Processing your message' notice", dl.sends)
	}
	// draining the queue runs the followup on the cleared session (no turn for /new itself).
	if _, err := loop.processGroupMessages("web:demo"); err != nil {
		t.Fatalf("process followup: %v", err)
	}
	if len(rr.runs) != 1 || !contains(rr.runs[0].batch, "look into X") {
		t.Fatalf("followup not processed: %+v", rr.runs)
	}
}

// TestStickyTopicRoutesSubsequent: a set sticky #topic (the #topic nav command)
// overrides a subsequent message's own topic for dispatch (gated effectiveTopic
// parity) — without applying sticky_topic it was dead state.
func TestStickyTopicRoutesSubsequent(t *testing.T) {
	db, loop, rr := recLoop(t)
	_ = db.PutGroup(core.Group{Folder: "demo"})
	doSetRoutes(t, db, []core.Route{{Match: "platform=slack", Target: "demo"}})
	if err := db.SetStickyTopic("slack:T/C/X", "support"); err != nil {
		t.Fatalf("set sticky topic: %v", err)
	}
	_ = db.PutMessage(core.Message{ID: "a", ChatJID: "slack:T/C/X", Sender: "u",
		Content: "my question", Topic: "", Timestamp: time.Now().UTC(), Verb: "message"})

	if _, err := loop.processGroupMessages("slack:T/C/X"); err != nil {
		t.Fatalf("process: %v", err)
	}
	if len(rr.runs) != 1 {
		t.Fatalf("turns=%d want 1", len(rr.runs))
	}
	if rr.runs[0].topic != "support" {
		t.Fatalf("dispatched topic=%q want support (sticky override)", rr.runs[0].topic)
	}
}

// TestEngagementNormalizedOffEffectiveTopic: a set sticky #topic makes the
// engagement probe key on the sticky topic, not the message's raw topic (gated
// normalizes off effectiveTopic). Engagement on the sticky topic governs a bare
// inbound whose own topic is empty.
func TestEngagementNormalizedOffEffectiveTopic(t *testing.T) {
	db, loop, _ := recLoop(t)
	_ = db.PutGroup(core.Group{Folder: "eng"})
	_ = db.SetStickyTopic("slack:T/C/X", "support")
	// engagement claimed on the sticky topic for the chat → eng.
	_ = db.SetEngagement("slack:T/C/X", "support", "eng", time.Hour)
	// a bare inbound (empty own topic) — effectiveTopic resolves it to "support".
	last := core.Message{ID: "a", ChatJID: "slack:T/C/X", Sender: "u",
		Content: "follow-up", Topic: "", Timestamp: time.Now().UTC(), Verb: "message"}

	folder, ok := loop.resolveGroup("slack:T/C/X", last)
	if !ok || folder != "eng" {
		t.Fatalf("resolve=%q,%v want eng,true (engagement keyed on sticky topic)", folder, ok)
	}
}

// TestDelegatedReplyReturnsToOrigin: a delegated turn (trigger batch carries
// forwarded_from = origin JID) delivers its reply back to the ORIGIN chat, not
// the child folder JID the run addresses (gated deliverTo override parity).
func TestDelegatedReplyReturnsToOrigin(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	dl := &recDeliverer{}
	srv := NewServer(db, nil, dl, nil, 0, "")
	_ = db.PutGroup(core.Group{Folder: "root/eng"})
	// a delegated turn: the run addresses the child folder JID; return_to is the origin.
	db.PutTurnContext("t-deleg", "root/eng", "", "root/eng", "delegate", "slack:T/C/ORIGIN")

	status, _, row := srv.appendAndDeliver("t-deleg", "root/eng", "done", "", true)
	if status != 200 || row == nil {
		t.Fatalf("appendAndDeliver status=%d row=%v", status, row)
	}
	if row.ChatJID != "slack:T/C/ORIGIN" {
		t.Fatalf("bot row chat_jid=%q want slack:T/C/ORIGIN (return path)", row.ChatJID)
	}
	if len(dl.sends) != 1 || dl.sends[0].jid != "slack:T/C/ORIGIN" {
		t.Fatalf("delivered to %+v want origin slack:T/C/ORIGIN", dl.sends)
	}
}

// TestDelegatedReplyEngagesDispatchChat: a delegated reply DELIVERS to the
// origin (return path) but ENGAGES the dispatch chat (tc.ChatJID), mirroring
// gated's BumpEngagement(chatJid). Before the fix engagement was bumped on the
// return-substituted origin JID, so the dispatch chat never engaged and the
// origin chat engaged a folder that doesn't own it.
func TestDelegatedReplyEngagesDispatchChat(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	dl := &recDeliverer{}
	srv := NewServer(db, nil, dl, nil, time.Hour, "") // non-zero engagement TTL
	_ = db.PutGroup(core.Group{Folder: "root/eng"})
	// dispatch chat = web:root/eng; the run returns to a different origin JID.
	db.PutTurnContext("t-deleg", "root/eng", "", "web:root/eng", "delegate", "slack:T/C/ORIGIN")

	if status, _, _ := srv.appendAndDeliver("t-deleg", "root/eng", "done", "", true); status != 200 {
		t.Fatalf("appendAndDeliver status=%d", status)
	}
	// engagement is claimed on the DISPATCH chat, not the origin.
	if f, ok := db.Engaged("web:root/eng", ""); !ok || f != "root/eng" {
		t.Fatalf("dispatch chat engaged=%q,%v want root/eng,true", f, ok)
	}
	if _, ok := db.Engaged("slack:T/C/ORIGIN", ""); ok {
		t.Fatal("engagement leaked onto the return-substituted origin chat")
	}
}

// TestSetLastReplyPreservesEngagement: a broadcast send from a different group
// must not hijack the engagement window set by SetEngagement. Regression for
// the v0.52.0 routing glitch: happy's migration broadcast called
// SetLastReply("telegram:user/X","","id","happy") which clobbered
// engaged_folder="krons" → "happy" while leaving engaged_until intact.
func TestSetLastReplyPreservesEngagement(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	jid := "telegram:user/1112184352"
	// krons claims engagement on the DM (normal reply path).
	if err := db.SetEngagement(jid, "", "krons", time.Hour); err != nil {
		t.Fatal(err)
	}
	// happy's migration broadcast calls SetLastReply on the same DM with a different folder.
	if err := db.SetLastReply(jid, "", "msg-broadcast", "happy"); err != nil {
		t.Fatal(err)
	}
	// engaged_folder must still be "krons" — the broadcast must not hijack it.
	folder, ok := db.Engaged(jid, "")
	if !ok || folder != "krons" {
		t.Fatalf("Engaged=%q,%v after broadcast SetLastReply; want krons,true", folder, ok)
	}
	// but last_reply_id must have updated (that's SetLastReply's job).
	if id := db.LastReplyID(jid, ""); id != "msg-broadcast" {
		t.Fatalf("LastReplyID=%q after SetLastReply; want msg-broadcast", id)
	}
}
