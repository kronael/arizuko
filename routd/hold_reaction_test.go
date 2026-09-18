package routd

import (
	"fmt"
	"testing"
	"time"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/core"
	apiv1 "github.com/kronael/arizuko/routd/api/v1"
)

// putHeldWithNotice records a held row already announced as noticeID.
func putHeldWithNotice(t *testing.T, d *DB, id, noticeID string) {
	t.Helper()
	if err := d.PutPendingAction(PendingAction{
		ID: id, GroupFolder: "atlas", CallerAgent: "folder:atlas", Tool: "mcp:like",
		Args: `{}`, ChatJID: "tg:1", NoticeID: noticeID,
	}); err != nil {
		t.Fatal(err)
	}
}

// TestVerdictForReaction_GreenAndRedOnly — the vocabulary is the whole security
// boundary here. 👍 is an ordinary social reaction, and reading one as "run the
// gated call" is an approval nobody gave.
func TestVerdictForReaction_GreenAndRedOnly(t *testing.T) {
	d := memDB(t)
	putHeldWithNotice(t, d, "ab12", "m7")

	for _, c := range []struct{ emoji, want string }{
		{"✅", "/approve ab12"},
		{"🟢", "/approve ab12"},
		{"❌", "/reject ab12"},
		{"🔴", "/reject ab12"},
		{"⛔", "/reject ab12"},
		{"👍", ""},
		{"👎", ""},
		{"🎉", ""},
		{"", ""},
	} {
		if got := d.VerdictForReaction("tg:1", "m7", c.emoji); got != c.want {
			t.Errorf("reaction %q → %q, want %q", c.emoji, got, c.want)
		}
	}
}

// TestVerdictForReaction_BoundToTheNoticeAndTheChat — a reaction anywhere else
// is just a reaction. Without the chat bound, a ✅ on a message that happens to
// share a platform id in another chat would decide someone else's call.
func TestVerdictForReaction_BoundToTheNoticeAndTheChat(t *testing.T) {
	d := memDB(t)
	putHeldWithNotice(t, d, "ab12", "m7")

	if got := d.VerdictForReaction("tg:1", "other-message", "✅"); got != "" {
		t.Errorf("a reaction on another message resolved %q", got)
	}
	if got := d.VerdictForReaction("tg:elsewhere", "m7", "✅"); got != "" {
		t.Errorf("a reaction in another chat resolved %q", got)
	}
	if got := d.VerdictForReaction("tg:1", "", "✅"); got != "" {
		t.Errorf("a reaction targeting nothing resolved %q", got)
	}
}

// TestVerdictForReaction_TakesEachAdapterAtItsWord — teled reports a reacted
// message as `<chat-jid>/<id>` while discd and slakd report the id alone, and
// the delivery always returns the bare id. Without the reduction, Telegram — the
// platform this actually runs on — would never match its own notice.
func TestVerdictForReaction_TakesEachAdapterAtItsWord(t *testing.T) {
	d := memDB(t)
	putHeldWithNotice(t, d, "ab12", "6335")

	for _, target := range []string{
		"6335",                           // discd, slakd
		"telegram:user/1112184352/6335",  // teled
		"telegram:group/5229089286/6335", // teled, group
	} {
		if got := d.VerdictForReaction("tg:1", target, "✅"); got != "/approve ab12" {
			t.Errorf("target %q → %q, want /approve ab12", target, got)
		}
	}
}

// TestVerdictForReaction_OnlyWhileHeld — a decided call is decided. Reacting to
// an old notice must not reopen or re-resolve it.
func TestVerdictForReaction_OnlyWhileHeld(t *testing.T) {
	d := memDB(t)
	putHeldWithNotice(t, d, "ab12", "m7")
	if _, err := resolvePending(d, "ab12", PendingApproved, "google:op", ""); err != nil {
		t.Fatal(err)
	}
	if got := d.VerdictForReaction("tg:1", "m7", "❌"); got != "" {
		t.Errorf("a reaction on an already-decided call resolved %q", got)
	}
}

// TestNotifyHeld_RecordsTheNoticeID — the reaction path exists only because the
// row knows which message announced it. The notice is delivered straight to the
// adapter and never becomes a `messages` row, so nothing else can supply it.
func TestNotifyHeld_RecordsTheNoticeID(t *testing.T) {
	waitForVerdict(t)

	d := memDB(t)
	dl := &recDeliverer{pid: "platform-77"}
	srv := &Server{db: d, deliver: dl}
	holdRule(t, d, "mcp:like", auth.EffectHoldBlock)
	gate := srv.holdGate(turnMCP{folder: "atlas", chatJID: "tg:1", turnID: "t1"})

	// The call is still waiting while this runs: a reaction resolves a HELD row,
	// and a row the waiter has already given up on is not one.
	go gate("mcp:like", map[string]any{"platform_id": "m1"})
	id := awaitHeldID(t, d)
	// The row lands before the notice does: the gate records the held call, then
	// delivers the notice and stamps the id the adapter answered. Two writes, so
	// the id arrives after the row.
	awaitNoticeID(t, d, id, "platform-77")
	if got := d.VerdictForReaction("tg:1", "platform-77", "✅"); got != "/approve "+id {
		t.Errorf("a reaction on the notice resolved %q", got)
	}
}

// TestReactionVerdict_RunsTheOrdinaryCommandPath — end to end from the gesture:
// a ✅ becomes `/approve <id>` and is consumed by the SAME parser and the SAME
// operator gate a typed command meets. No second resolution path exists.
func TestReactionVerdict_RunsTheOrdinaryCommandPath(t *testing.T) {
	db, loop, _ := recLoop(t)
	loop.deliver = &recDeliverer{}
	if err := addMembership(db, "u", "role:operator", "test"); err != nil {
		t.Fatal(err)
	}
	putHeldWithNotice(t, db, "ab12", "m7")

	cmd := db.VerdictForReaction("tg:1", "m7", "✅")
	if !loop.steer("tg:1", core.Message{
		ChatJID: "tg:1", Sender: "u", Content: cmd, Timestamp: time.Now().UTC(),
	}, "atlas") {
		t.Fatalf("the reaction's command %q was not consumed", cmd)
	}
	row, _ := pendingActionRow(db, "ab12")
	if row.Status != PendingApproved || row.ReviewedBy != "u" {
		t.Fatalf("want approved by u, got %+v", row)
	}
}

// TestReactionVerdict_BystanderIsRefused — the gesture inherits the gate, it
// does not bypass it. A reaction is a thing anyone in the chat can leave, and
// the gate asks whether that person holds admin on the held call's folder.
func TestReactionVerdict_BystanderIsRefused(t *testing.T) {
	db, loop, _ := recLoop(t)
	loop.deliver = &recDeliverer{}
	putHeldWithNotice(t, db, "ab12", "m7")

	cmd := db.VerdictForReaction("tg:1", "m7", "✅")
	loop.steer("tg:1", core.Message{
		ChatJID: "tg:1", Sender: "bystander", Content: cmd, Timestamp: time.Now().UTC(),
	}, "atlas")
	row, _ := pendingActionRow(db, "ab12")
	if row.Status != PendingHeld {
		t.Fatalf("a bystander's reaction decided the call: %+v", row)
	}
}

// TestIngress_RewritesAReactionIntoItsVerdict is the ingest half: the rewrite
// has to happen at /v1/messages and not in steer, because steer runs AFTER route
// resolution and a reaction on a mention-only channel never gets that far. The
// stored row must therefore already read as the command.
func TestIngress_RewritesAReactionIntoItsVerdict(t *testing.T) {
	db, h := authSrv(t, nil)
	putHeldWithNotice(t, db, "ab12", "m7")

	rec := doJSON(t, h, "POST", "/v1/messages", "", apiv1.Message{
		ID: "react-1", ChatJID: "tg:1", Sender: "u", Content: "✅",
		ReplyTo: "m7", Verb: "like",
	})
	if rec.Code != 200 {
		t.Fatalf("ingress = %d body=%s", rec.Code, rec.Body.String())
	}
	rewritten := msgByID(t, db, "tg:1", "react-1")
	content, verb := rewritten.Content, rewritten.Verb
	if content != "/approve ab12" {
		t.Errorf("stored content = %q, want the command the reaction stands for", content)
	}
	if verb == "like" {
		t.Error("the rewritten row must not stay verb=like, or a mention-only route drops the verdict")
	}
}

// TestIngress_LeavesAnOrdinaryReactionAlone — the rewrite is narrow. A reaction
// on anything but a held notice stays exactly what the user left.
func TestIngress_LeavesAnOrdinaryReactionAlone(t *testing.T) {
	db, h := authSrv(t, nil)
	putHeldWithNotice(t, db, "ab12", "m7")

	for _, c := range []struct{ id, emoji, replyTo string }{
		{"r-thumbs", "👍", "m7"},
		{"r-elsewhere", "✅", "some-other-message"},
	} {
		if rec := doJSON(t, h, "POST", "/v1/messages", "", apiv1.Message{
			ID: c.id, ChatJID: "tg:1", Sender: "u", Content: c.emoji,
			ReplyTo: c.replyTo, Verb: "like",
		}); rec.Code != 200 {
			t.Fatalf("%s ingress = %d", c.id, rec.Code)
		}
		content := msgByID(t, db, "tg:1", c.id).Content
		if content != c.emoji {
			t.Errorf("%s stored %q, want the reaction untouched", c.id, content)
		}
	}
}

// awaitNoticeID waits for the second write — the notice id the delivery
// answered, stamped onto a row that already exists.
func awaitNoticeID(t *testing.T, d *DB, id, want string) {
	t.Helper()
	awaitCond(t, fmt.Sprintf("notice id never became %q — the delivery never stamped the row", want), func() bool {
		row, ok := pendingActionRow(d, id)
		return ok && row.NoticeID == want
	})
}
