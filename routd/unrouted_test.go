package routd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	apiv1 "github.com/kronael/arizuko/routd/api/v1"
)

func getUnrouted(t *testing.T, h http.Handler) (int, apiv1.UnroutedChatsResponse) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/v1/routing/unrouted", nil))
	var out apiv1.UnroutedChatsResponse
	if rec.Code == 200 {
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode: %v", err)
		}
	}
	return rec.Code, out
}

// TestUnroutedChats_HeldIsListedRoutedIsNot pins spec 6/35-unrouted-traffic's rule from both
// sides. A chat no route claims is held: no turn, and it is listed. A chat an
// explicit route claims is absent from the list AND still dispatches to its
// folder — an instance with routes today keeps routing exactly as it does now.
// A bot-only chat and a direct-address web: chat are not traffic waiting for
// a route and never appear.
func TestUnroutedChats_HeldIsListedRoutedIsNot(t *testing.T) {
	db, srv, runner := newTestRoutd(t)
	h := srv.Handler()
	if err := db.PutGroup(core.Group{Folder: "demo"}); err != nil {
		t.Fatal(err)
	}
	doJSON(t, h, "PUT", "/v1/routes", "", map[string]any{"routes": []apiv1.Route{
		{Match: "chat_jid=telegram:group/1", Target: "demo"},
	}})
	now := time.Now().UTC()
	put := func(m core.Message) {
		t.Helper()
		if err := db.PutMessage(m); err != nil {
			t.Fatal(err)
		}
	}
	put(core.Message{ID: "r1", ChatJID: "telegram:group/1", Sender: "u1", Content: "routed", Timestamp: now, Verb: "message"})
	put(core.Message{ID: "h1", ChatJID: "telegram:user/7", Sender: "u7", ChatName: "Seven", Content: "hello?", Timestamp: now.Add(-time.Minute), Verb: "message"})
	put(core.Message{ID: "h2", ChatJID: "telegram:user/7", Sender: "u7", ChatName: "Seven", Content: "anyone?", Timestamp: now.Add(time.Second), Verb: "message"})
	put(core.Message{ID: "b1", ChatJID: "telegram:user/9", Sender: "bot", Content: "proactive", Timestamp: now, BotMsg: true})
	put(core.Message{ID: "w1", ChatJID: "web:demo", Sender: "u", Content: "direct", Timestamp: now, Verb: "message"})

	// The loop's own verdicts: the routed chat dispatches, the held one drops.
	if _, err := srv.loop.processGroupMessages("telegram:group/1"); err != nil {
		t.Fatal(err)
	}
	if runner.gotTurn != "r1" {
		t.Fatalf("routed chat dispatched turn %q, want r1", runner.gotTurn)
	}
	if _, err := srv.loop.processGroupMessages("telegram:user/7"); err != nil {
		t.Fatal(err)
	}
	if runner.gotTurn != "r1" {
		t.Fatalf("held chat fired a turn: %q", runner.gotTurn)
	}

	code, out := getUnrouted(t, h)
	if code != 200 {
		t.Fatalf("status = %d", code)
	}
	if len(out.Chats) != 1 {
		t.Fatalf("unrouted = %+v, want exactly telegram:user/7", out.Chats)
	}
	c := out.Chats[0]
	if c.ChatJID != "telegram:user/7" || c.Platform != "telegram" || c.Count != 2 ||
		c.LastSender != "u7" || c.ChatName != "Seven" {
		t.Errorf("row = %+v", c)
	}
	first, _ := time.Parse(time.RFC3339Nano, c.FirstAt)
	last, _ := time.Parse(time.RFC3339Nano, c.LastAt)
	if !first.Before(last) {
		t.Errorf("first_at %s is not before last_at %s", c.FirstAt, c.LastAt)
	}

	// Claiming the chat is a route write, and the hold ends with it.
	doJSON(t, h, "PUT", "/v1/routes", "", map[string]any{"routes": []apiv1.Route{
		{Match: "chat_jid=telegram:group/1", Target: "demo"},
		{Match: "chat_jid=telegram:user/7", Target: "demo"},
	}})
	if _, out := getUnrouted(t, h); len(out.Chats) != 0 {
		t.Errorf("after the claim: %+v, want none", out.Chats)
	}
}

// TestUnroutedChats_ObserveRouteIsAClaim — a #observe catch-all consumes the
// miss (5/18 staging posture), so a chat it captures is not held.
func TestUnroutedChats_ObserveRouteIsAClaim(t *testing.T) {
	db, srv, _ := newTestRoutd(t)
	h := srv.Handler()
	if err := db.PutGroup(core.Group{Folder: "staging"}); err != nil {
		t.Fatal(err)
	}
	doJSON(t, h, "PUT", "/v1/routes", "", map[string]any{"routes": []apiv1.Route{
		{Match: "platform=telegram", Target: "staging#observe"},
	}})
	if err := db.PutMessage(core.Message{ID: "o1", ChatJID: "telegram:user/3", Sender: "u3",
		Content: "hi", Timestamp: time.Now().UTC(), Verb: "message"}); err != nil {
		t.Fatal(err)
	}
	if _, out := getUnrouted(t, h); len(out.Chats) != 0 {
		t.Errorf("observed chat listed as held: %+v", out.Chats)
	}
}

// TestUnroutedChats_FolderScopedTokenRefused — a held chat belongs to no
// folder, so a folder-scoped bearer owns none of them and is refused outright;
// an unscoped service bearer holding routes:read lists them.
func TestUnroutedChats_FolderScopedTokenRefused(t *testing.T) {
	_, srv, _ := newTestRoutd(t)
	srv.verify = fakeVerifier{sub: "agent", scope: []string{"routes:read:own_group"}, folder: "demo"}
	if code, _ := getUnrouted(t, srv.Handler()); code != 403 {
		t.Fatalf("folder-scoped token = %d, want 403", code)
	}
	srv.verify = fakeVerifier{sub: "service:dashd", scope: []string{"routes:read"}}
	if code, _ := getUnrouted(t, srv.Handler()); code != 200 {
		t.Fatalf("service token = %d, want 200", code)
	}
	srv.verify = fakeVerifier{sub: "user:nobody", scope: []string{"audit:read"}}
	if code, _ := getUnrouted(t, srv.Handler()); code != 403 {
		t.Fatalf("token without routes:read = %d, want 403", code)
	}
}

// TestUnroutedChats_NoLoopIsUnavailable — without the loop there is no resolve
// to ask, and the answer is 503, not an empty list.
func TestUnroutedChats_NoLoopIsUnavailable(t *testing.T) {
	_, _, h := dashSrv(t)
	if code, _ := getUnrouted(t, h); code != 503 {
		t.Fatalf("no loop = %d, want 503", code)
	}
}
