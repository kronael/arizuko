package routd

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/core"
	apiv1 "github.com/kronael/arizuko/routd/api/v1"
	"github.com/kronael/arizuko/store"
)

// holdOneCall arms a hold rule on the folder and suspends one `mcp:like` call
// through the REAL gate, returning the pending id.
func holdOneCall(t *testing.T, db *DB, srv *Server, folder, chatJID string) string {
	t.Helper()
	if err := addACLRow(db, core.ACLRow{
		Principal: "folder:" + folder, Action: auth.HoldPrefix + "mcp:like",
		Scope: folder, Effect: auth.EffectHoldAsync,
	}); err != nil {
		t.Fatalf("arm the hold rule: %v", err)
	}
	gate := srv.holdGate(turnMCP{folder: folder, chatJID: chatJID})
	if gate == nil {
		t.Fatal("a normal folder turn must get a gate")
	}
	id, held := gate("mcp:like", map[string]any{"platform_id": "p1", "emoji": "👍"})
	if !held || id == "" {
		t.Fatalf("the hold rule must suspend the call (held=%v id=%q)", held, id)
	}
	return id
}

// approveInChat runs the chat `/approve <id>` command — the same
// cmdResolveHold both the typed command and the dashboard button reach.
func approveInChat(t *testing.T, db *DB, srv *Server, chatJID, folder, id string) {
	t.Helper()
	if err := addMembership(db, "google:op", "role:operator", "test"); err != nil {
		t.Fatalf("make the reviewer an operator: %v", err)
	}
	srv.loop.cmdResolveHold(chatJID, folder, id, "google:op", PendingApproved, false)
	p, ok := pendingActionRow(db, id)
	if !ok || p.Status != PendingApproved {
		t.Fatalf("the verdict did not commit: %+v ok=%v", p, ok)
	}
}

// TestApproveReIssuesHeldCallInTheCursorSecond is BUGS L97 end to end.
//
// The operator types /approve, the verdict commits, and the re-issue trigger is
// written with nanosecond precision. The chat's cursor is a platform timestamp,
// which every adapter truncates to the second — so the trigger and the cursor
// share a second, and only a fixed-width layout keeps the trigger on the right
// side of `timestamp > agent_cursor`. Under a fraction-stripping layout the
// dispatch scan steps over it and the held call is never re-issued.
func TestApproveReIssuesHeldCallInTheCursorSecond(t *testing.T) {
	db, srv, runner := newTestRoutd(t)
	const chatJID = "slack:T/C/U"
	if err := db.PutGroup(core.Group{Folder: "atlas"}); err != nil {
		t.Fatalf("put group: %v", err)
	}
	doJSON(t, srv.Handler(), "PUT", "/v1/routes", "",
		map[string]any{"routes": []apiv1.Route{{Match: "platform=slack", Target: "atlas"}}})

	id := holdOneCall(t, db, srv, "atlas", chatJID)
	approveInChat(t, db, srv, chatJID, "atlas", id)

	// The trigger row the approval wrote.
	trigger := triggerRow(t, db, id)

	// The adapter's half: a platform timestamp in the trigger's own second,
	// stored as the chat's cursor. This is the collision L97 reports, and it is
	// the normal case — the verdict and the platform row are the same second.
	cursorAt := trigger.Timestamp.Truncate(time.Second)
	if err := db.SetAgentCursor(chatJID, core.TS(cursorAt)); err != nil {
		t.Fatalf("set cursor: %v", err)
	}

	// The dispatch scan must see the trigger across that cursor.
	msgs, err := db.MessagesSince(chatJID, db.GetAgentCursor(chatJID))
	if err != nil {
		t.Fatalf("messages since cursor: %v", err)
	}
	if !containsID(msgs, trigger.ID) {
		t.Fatalf("the re-issue trigger is invisible to the dispatch feed: "+
			"trigger %q vs cursor %q", core.TS(trigger.Timestamp), db.GetAgentCursor(chatJID))
	}

	// And the turn actually runs with the re-issue instruction in its batch.
	if _, err := srv.loop.processGroupMessages(chatJID); err != nil {
		t.Fatalf("process: %v", err)
	}
	if !strings.Contains(runner.gotBatch, "re-issue mcp:like") {
		t.Fatalf("the agent's batch carries no re-issue instruction: %q", runner.gotBatch)
	}

	// The re-issued call passes the gate exactly once — the approval is spent.
	gate := srv.holdGate(turnMCP{folder: "atlas", chatJID: chatJID})
	if _, held := gate("mcp:like", map[string]any{"platform_id": "p1", "emoji": "👍"}); held {
		t.Fatal("the approved re-issue must run, not be held again")
	}
}

// TestPollOnceDoesNotSkipTheChatHoldingATrigger covers the other gate on the
// same second: pollOnce feeds from the global minimum cursor and skips any chat
// already current on its own, comparing the batch tail against that cursor as
// text. The trigger is the tail, so a stripped fraction makes the chat look
// current and it is never enqueued.
func TestPollOnceDoesNotSkipTheChatHoldingATrigger(t *testing.T) {
	db, srv, runner := newTestRoutd(t)
	const chatJID = "slack:T/C/U"
	if err := db.PutGroup(core.Group{Folder: "atlas"}); err != nil {
		t.Fatalf("put group: %v", err)
	}
	doJSON(t, srv.Handler(), "PUT", "/v1/routes", "",
		map[string]any{"routes": []apiv1.Route{{Match: "platform=slack", Target: "atlas"}}})

	id := holdOneCall(t, db, srv, "atlas", chatJID)
	approveInChat(t, db, srv, chatJID, "atlas", id)

	trigger := triggerRow(t, db, id)
	if err := db.SetAgentCursor(chatJID, core.TS(trigger.Timestamp.Truncate(time.Second))); err != nil {
		t.Fatalf("set cursor: %v", err)
	}

	// pollOnce enqueues; the queue is stopped in this harness, so drive the
	// dispatch the enqueue would have reached and assert the turn ran.
	srv.loop.pollOnce()
	if _, err := srv.loop.processGroupMessages(chatJID); err != nil {
		t.Fatalf("process: %v", err)
	}
	if runner.gotTurn != trigger.ID {
		t.Fatalf("the poll skipped the chat holding the trigger: turn_id=%q want %q",
			runner.gotTurn, trigger.ID)
	}
}

// TestMigration0044NormalisesTheCursorColumns pins the other half: the code fix
// alone does not reach a deployed instance, whose chats hold cursors written at
// whatever width they were written. A nanosecond row arriving in the second a
// stored whole-second cursor names still sorts behind it.
func TestMigration0044NormalisesTheCursorColumns(t *testing.T) {
	db := memDB(t)
	const chatJID = "slack:T/C/U"

	// The state a pre-migration instance holds: a whole-second cursor and a
	// nanosecond row in that same second.
	if _, err := db.SQL().Exec(
		`INSERT INTO chats (jid, agent_cursor) VALUES (?, '2026-08-26T19:06:41Z')`, chatJID,
	); err != nil {
		t.Fatalf("seed legacy cursor: %v", err)
	}
	if _, err := db.SQL().Exec(
		`INSERT INTO messages (id, chat_jid, sender, sender_name, content, timestamp,
		    is_from_me, is_bot_message, reply_to_id, reply_to_text, reply_to_sender,
		    topic, routed_to, verb, attachments, source, is_observed, status, chat_name)
		 VALUES ('hitl-legacy', ?, 'google:op', '', 'Approved held call',
		    '2026-08-26T19:06:41.882458214Z', 0, 0, '', '', '', '', '', 'message', '', '', 0, 'sent', '')`,
		chatJID,
	); err != nil {
		t.Fatalf("seed legacy trigger: %v", err)
	}
	// A row nothing in the codebase can produce: it must survive untouched
	// rather than have a zone invented for it.
	if _, err := db.SQL().Exec(
		`INSERT INTO messages (id, chat_jid, sender, sender_name, content, timestamp,
		    is_from_me, is_bot_message, reply_to_id, reply_to_text, reply_to_sender,
		    topic, routed_to, verb, attachments, source, is_observed, status, chat_name)
		 VALUES ('hand-written', ?, 'u1', '', 'poked in by hand',
		    '2026-04-02 12:38:47', 0, 0, '', '', '', '', '', 'message', '', '', 0, 'sent', '')`,
		chatJID,
	); err != nil {
		t.Fatalf("seed malformed row: %v", err)
	}
	if _, err := db.SQL().Exec(
		`INSERT INTO sessions (group_folder, topic, session_id, observed_cursor)
		 VALUES ('atlas', 'main', 's1', '2026-08-26T19:06:41Z')`,
	); err != nil {
		t.Fatalf("seed legacy observed cursor: %v", err)
	}

	msgs, err := db.MessagesSince(chatJID, db.GetAgentCursor(chatJID))
	if err != nil {
		t.Fatalf("messages since: %v", err)
	}
	if containsID(msgs, "hitl-legacy") {
		t.Fatal("precondition: the legacy trigger must start out invisible")
	}

	applyMigration(t, db, "migrations/0044-fixed-width-timestamps.sql")

	msgs, err = db.MessagesSince(chatJID, db.GetAgentCursor(chatJID))
	if err != nil {
		t.Fatalf("messages since: %v", err)
	}
	if !containsID(msgs, "hitl-legacy") {
		t.Fatalf("the migration left the legacy trigger behind the cursor %q",
			db.GetAgentCursor(chatJID))
	}

	for col, want := range map[string]string{
		"(SELECT agent_cursor FROM chats WHERE jid = '" + chatJID + "')":                     "2026-08-26T19:06:41.000000000Z",
		"(SELECT timestamp FROM messages WHERE id = 'hitl-legacy')":                          "2026-08-26T19:06:41.882458214Z",
		"(SELECT timestamp FROM messages WHERE id = 'hand-written')":                         "2026-04-02 12:38:47",
		"(SELECT observed_cursor FROM sessions WHERE group_folder='atlas' AND topic='main')": "2026-08-26T19:06:41.000000000Z",
	} {
		var got string
		if err := db.SQL().QueryRow("SELECT " + col).Scan(&got); err != nil {
			t.Fatalf("read %s: %v", col, err)
		}
		if got != want {
			t.Errorf("%s = %q, want %q", col, got, want)
		}
	}

	// Idempotent: a second run must not move anything.
	applyMigration(t, db, "migrations/0044-fixed-width-timestamps.sql")
	var ts string
	if err := db.SQL().QueryRow(
		"SELECT timestamp FROM messages WHERE id = 'hitl-legacy'").Scan(&ts); err != nil {
		t.Fatalf("read after rerun: %v", err)
	}
	if ts != "2026-08-26T19:06:41.882458214Z" {
		t.Errorf("a second run rewrote a normalised row to %q", ts)
	}
}

func containsID(msgs []core.Message, id string) bool {
	for _, m := range msgs {
		if m.ID == id {
			return true
		}
	}
	return false
}

// triggerRow reads the re-issue trigger a verdict wrote for a pending id. The
// row id is core.MsgID("hitl-"+id), so the pending id is its prefix.
func triggerRow(t *testing.T, db *DB, pendingID string) core.Message {
	t.Helper()
	var rowID string
	if err := db.SQL().QueryRow(
		`SELECT id FROM messages WHERE id LIKE 'hitl-' || ? || '-%'`, pendingID,
	).Scan(&rowID); err != nil {
		t.Fatalf("no re-issue trigger row for %s: %v", pendingID, err)
	}
	m, ok := store.New(db.SQL()).MessageByID(rowID)
	if !ok {
		t.Fatalf("trigger row %s vanished", rowID)
	}
	return m
}

// applyMigration runs a SHIPPED migration file. Reading the file rather than
// inlining its SQL is what keeps the test from passing against an empty one.
func applyMigration(t *testing.T, db *DB, path string) {
	t.Helper()
	sqlBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if _, err := db.SQL().Exec(string(sqlBytes)); err != nil {
		t.Fatalf("apply %s: %v", path, err)
	}
}
