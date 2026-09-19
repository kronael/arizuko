package routd

import (
	"testing"

	"github.com/kronael/arizuko/core"
	apiv1 "github.com/kronael/arizuko/routd/api/v1"
)

// stated is the classification an adapter gives; a caller with none gives nil.
func stated(v bool) *bool { return &v }

// A chat is classified by the adapter that OBSERVES it, and by nothing else.
//
// timed's cron fire posts into a chat it only writes to — `fireSplit` builds an
// apiv1.Message carrying id, chat_jid, sender, content and timestamp — so it
// states no classification. Reading that silence as "DM" cleared
// `chats.is_group` on every fire, and `turns.go` then answered a live group in
// a direct chat's reply shape: 37 messages from 11 senders in one telegram
// group with the flag at 0 (BUGS L306).
func TestCronFireLeavesTheChatClassification(t *testing.T) {
	db, srv, _ := newTestRoutd(t)
	h := srv.Handler()
	_ = db.PutGroup(core.Group{Folder: "demo"})
	doJSON(t, h, "PUT", "/v1/routes", "", map[string]any{
		"routes": []apiv1.Route{{Match: "platform=telegram", Target: "demo"}}})
	const jid = "telegram:group/1003805633088"

	post := func(what string, m apiv1.Message) {
		t.Helper()
		if rec := doJSON(t, h, "POST", "/v1/messages", "", m); rec.Code != 200 {
			t.Fatalf("%s: status %d: %s", what, rec.Code, rec.Body.String())
		}
	}

	post("adapter inbound", apiv1.Message{ID: "m1", ChatJID: jid,
		Sender: "telegram:user/1", Content: "hi", IsGroup: stated(true)})
	if !db.ChatIsGroup(jid) {
		t.Fatal("the adapter said the chat is multi-party and the flag did not take")
	}

	post("cron fire", apiv1.Message{ID: "sched-demo-mem@2026-09-19T06:00:00Z", ChatJID: jid,
		Sender: "timed", Content: "run the memory task", Timestamp: 1758261600})
	if !db.ChatIsGroup(jid) {
		t.Fatal("a cron fire unflagged a group chat: a caller that states no " +
			"classification must not restate one")
	}

	// The adapter's own "this is a DM" still lands: its false is an answer, and
	// it is what takes a wrong `true` back off a chat routd saw misclassified.
	post("adapter says DM", apiv1.Message{ID: "m2", ChatJID: jid,
		Sender: "telegram:user/1", Content: "hi", IsGroup: stated(false)})
	if db.ChatIsGroup(jid) {
		t.Fatal("the adapter said the chat is a DM and the flag was not cleared")
	}
}

// The chat row is opened by the message append, so a fire into a chat nobody
// has classified still leaves the instance a record that it saw the chat — the
// partition list every whole-history reader walks.
func TestCronFireOpensAnUnclassifiedChat(t *testing.T) {
	db, srv, _ := newTestRoutd(t)
	h := srv.Handler()
	_ = db.PutGroup(core.Group{Folder: "demo"})
	doJSON(t, h, "PUT", "/v1/routes", "", map[string]any{
		"routes": []apiv1.Route{{Match: "platform=telegram", Target: "demo"}}})
	const jid = "telegram:group/5125571890"

	if rec := doJSON(t, h, "POST", "/v1/messages", "", apiv1.Message{
		ID: "sched-demo-brief@2026-09-19T07:00:00Z", ChatJID: jid, Sender: "timed",
		Content: "brief me", Timestamp: 1758265200}); rec.Code != 200 {
		t.Fatalf("cron fire: status %d: %s", rec.Code, rec.Body.String())
	}
	jids, err := db.ChatJIDs()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, j := range jids {
		if j == jid {
			found = true
		}
	}
	if !found {
		t.Fatalf("the chats partition is %v and does not name %s: a message sits in a "+
			"chat nothing feeds and nothing exports", jids, jid)
	}
	if db.ChatIsGroup(jid) {
		t.Error("a chat nobody classified reads as multi-party")
	}
}
