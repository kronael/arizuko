package main

// coverage_test.go — high-value tests for teled paths not covered elsewhere:
//   handleReaction, threadMsgID, Pin, Unpin, UnpinAll, Post (text + media),
//   Forward happy-path.
//
// All use the existing newTestBot / tgMock / teledRouter helpers from
// integration_test.go (same package).

import (
	"errors"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tgbotapi "github.com/matterbridge/telegram-bot-api/v6"

	"github.com/kronael/arizuko/chanlib"
	"github.com/kronael/arizuko/tests/testutils"
)

// ---------------------------------------------------------------------------
// threadMsgID
// ---------------------------------------------------------------------------

func TestThreadMsgID_Valid(t *testing.T) {
	if got := threadMsgID("123"); got != 123 {
		t.Errorf("threadMsgID(\"123\") = %d, want 123", got)
	}
}

func TestThreadMsgID_Empty(t *testing.T) {
	if got := threadMsgID(""); got != 0 {
		t.Errorf("threadMsgID(\"\") = %d, want 0", got)
	}
}

func TestThreadMsgID_NotANumber(t *testing.T) {
	if got := threadMsgID("abc"); got != 0 {
		t.Errorf("threadMsgID(\"abc\") = %d, want 0", got)
	}
}

// ---------------------------------------------------------------------------
// handleReaction
// ---------------------------------------------------------------------------

func TestHandleReaction_Dispatches(t *testing.T) {
	mr := newTeledRouterMock()
	defer mr.close()
	rc := chanlib.NewRouterClient(mr.srv.URL)
	rc.SetToken("tok")

	m := newTGMock()
	defer m.close()
	b := newTestBot(t, m, config{Name: "telegram"})
	defer b.typing.Stop()

	r := &messageReactionUpdated{
		MessageID:   42,
		Date:        int64(time.Now().Unix()),
		OldReaction: []reactionType{},
		NewReaction: []reactionType{{Type: "emoji", Emoji: "👍"}},
		User:        &tgbotapi.User{ID: 99, FirstName: "Alice"},
	}
	r.Chat.ID = -100123

	ok := b.handleReaction(r, rc)
	if !ok {
		t.Fatal("handleReaction returned false")
	}

	mr.mu.Lock()
	defer mr.mu.Unlock()
	if len(mr.msgs) != 1 {
		t.Fatalf("dispatched %d, want 1", len(mr.msgs))
	}
	msg := mr.msgs[0]
	if msg.Reaction != "👍" {
		t.Errorf("Reaction = %q, want 👍", msg.Reaction)
	}
	if msg.ReplyTo != "42" {
		t.Errorf("ReplyTo = %q, want 42", msg.ReplyTo)
	}
	if msg.Sender != "telegram:user/99" {
		t.Errorf("Sender = %q, want telegram:user/99", msg.Sender)
	}
	if msg.Verb != "like" {
		t.Errorf("Verb = %q, want like", msg.Verb)
	}
	// Group chat (negative chat ID)
	if !msg.IsGroup {
		t.Error("IsGroup should be true for group chat")
	}
}

func TestHandleReaction_SkipsExistingEmoji(t *testing.T) {
	// An emoji present in both old and new reactions is not a new addition.
	mr := newTeledRouterMock()
	defer mr.close()
	rc := chanlib.NewRouterClient(mr.srv.URL)
	rc.SetToken("tok")

	m := newTGMock()
	defer m.close()
	b := newTestBot(t, m, config{Name: "telegram"})
	defer b.typing.Stop()

	r := &messageReactionUpdated{
		MessageID:   5,
		Date:        int64(time.Now().Unix()),
		OldReaction: []reactionType{{Type: "emoji", Emoji: "👍"}},
		NewReaction: []reactionType{{Type: "emoji", Emoji: "👍"}},
		User:        &tgbotapi.User{ID: 1},
	}
	r.Chat.ID = 1

	b.handleReaction(r, rc)

	mr.mu.Lock()
	defer mr.mu.Unlock()
	if len(mr.msgs) != 0 {
		t.Errorf("unchanged emoji should not be dispatched, got %d", len(mr.msgs))
	}
}

func TestHandleReaction_SkipsNonEmojiType(t *testing.T) {
	mr := newTeledRouterMock()
	defer mr.close()
	rc := chanlib.NewRouterClient(mr.srv.URL)
	rc.SetToken("tok")

	m := newTGMock()
	defer m.close()
	b := newTestBot(t, m, config{Name: "telegram"})
	defer b.typing.Stop()

	r := &messageReactionUpdated{
		MessageID:   7,
		Date:        int64(time.Now().Unix()),
		OldReaction: []reactionType{},
		NewReaction: []reactionType{{Type: "custom_emoji", Emoji: ""}},
		User:        &tgbotapi.User{ID: 2},
	}
	r.Chat.ID = 1

	b.handleReaction(r, rc)

	mr.mu.Lock()
	defer mr.mu.Unlock()
	if len(mr.msgs) != 0 {
		t.Errorf("non-emoji reaction type should be skipped, got %d", len(mr.msgs))
	}
}

// ---------------------------------------------------------------------------
// Pin / Unpin / UnpinAll
// ---------------------------------------------------------------------------

// addFakePlatformGetMe registers the getMe stub every FakePlatform test needs.
func addFakePlatformGetMe(fp *testutils.FakePlatform) {
	fp.On("POST /bot"+testToken+"/getMe", func(testutils.PlatformReq) (int, any) {
		return 200, map[string]any{"ok": true, "result": map[string]any{
			"id": 1, "is_bot": true, "username": "TestBot", "first_name": "Test",
		}}
	})
}

// addPinOKResponse registers a stub that returns {"ok":true,"result":true}
// for the given Telegram method name on a FakePlatform.
func addPinOKResponse(fp *testutils.FakePlatform, method string) {
	fp.On("POST /bot"+testToken+"/"+method, func(testutils.PlatformReq) (int, any) {
		return 200, map[string]any{"ok": true, "result": true}
	})
}

func TestPin_HappyPath(t *testing.T) {
	fp := testutils.NewFakePlatform()
	defer fp.Close()
	addFakePlatformGetMe(fp)
	addPinOKResponse(fp, "pinChatMessage")

	b := newTeledBotForPlatform(t, fp)
	defer b.typing.Stop()

	if err := b.Pin(chanlib.PinRequest{ChatJID: "telegram:555", TargetID: "42"}); err != nil {
		t.Fatalf("Pin: %v", err)
	}

	var found bool
	for _, r := range fp.Requests() {
		if r.Method == "POST" && strings.HasSuffix(r.Path, "/pinChatMessage") {
			found = true
			v, _ := url.ParseQuery(string(r.Body))
			if v.Get("chat_id") != "555" {
				t.Errorf("chat_id = %q, want 555", v.Get("chat_id"))
			}
			if v.Get("message_id") != "42" {
				t.Errorf("message_id = %q, want 42", v.Get("message_id"))
			}
		}
	}
	if !found {
		t.Errorf("pinChatMessage not called; requests = %+v", fp.Requests())
	}
}

func TestUnpin_HappyPath(t *testing.T) {
	fp := testutils.NewFakePlatform()
	defer fp.Close()
	addFakePlatformGetMe(fp)
	addPinOKResponse(fp, "unpinChatMessage")

	b := newTeledBotForPlatform(t, fp)
	defer b.typing.Stop()

	if err := b.Unpin(chanlib.UnpinRequest{ChatJID: "telegram:555", TargetID: "42"}); err != nil {
		t.Fatalf("Unpin: %v", err)
	}

	var found bool
	for _, r := range fp.Requests() {
		if r.Method == "POST" && strings.HasSuffix(r.Path, "/unpinChatMessage") {
			found = true
		}
	}
	if !found {
		t.Errorf("unpinChatMessage not called; requests = %+v", fp.Requests())
	}
}

func TestUnpinAll_HappyPath(t *testing.T) {
	fp := testutils.NewFakePlatform()
	defer fp.Close()
	addFakePlatformGetMe(fp)
	addPinOKResponse(fp, "unpinAllChatMessages")

	b := newTeledBotForPlatform(t, fp)
	defer b.typing.Stop()

	if err := b.Unpin(chanlib.UnpinRequest{ChatJID: "telegram:555", All: true}); err != nil {
		t.Fatalf("UnpinAll: %v", err)
	}

	var found bool
	for _, r := range fp.Requests() {
		if r.Method == "POST" && strings.HasSuffix(r.Path, "/unpinAllChatMessages") {
			found = true
		}
	}
	if !found {
		t.Errorf("unpinAllChatMessages not called; requests = %+v", fp.Requests())
	}
}

// ---------------------------------------------------------------------------
// Post
// ---------------------------------------------------------------------------

func TestBotPost_TextOnlyRoutesThroughSend(t *testing.T) {
	// Post without media delegates to Send → creates a message.
	m := newTGMock()
	defer m.close()
	b := newTestBot(t, m, config{Name: "telegram"})
	defer b.typing.Stop()

	id, err := b.Post(chanlib.PostRequest{ChatJID: "telegram:123", Content: "broadcast"})
	if err != nil {
		t.Fatalf("Post: %v", err)
	}
	if id == "" {
		t.Error("Post should return non-empty id")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.lastSent) != 1 {
		t.Fatalf("lastSent = %d, want 1", len(m.lastSent))
	}
	if m.lastSent[0]["text"] != "broadcast" {
		t.Errorf("text = %v", m.lastSent[0]["text"])
	}
}

func TestBotPost_WithMediaReturnsUnsupported(t *testing.T) {
	b := &bot{}
	_, err := b.Post(chanlib.PostRequest{
		ChatJID:    "telegram:123",
		Content:    "caption",
		MediaPaths: []string{"/tmp/img.jpg"},
	})
	if err == nil {
		t.Fatal("expected error for media post")
	}
	ue, ok := err.(*chanlib.UnsupportedError)
	if !ok || ue.Hint == "" {
		t.Errorf("expected UnsupportedError with hint, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Forward happy-path
// ---------------------------------------------------------------------------

func TestBotForward_HappyPath(t *testing.T) {
	fp := testutils.NewFakePlatform()
	defer fp.Close()
	addFakePlatformGetMe(fp)
	var fwdHits atomic.Int32
	fp.On("POST /bot"+testToken+"/forwardMessage", func(req testutils.PlatformReq) (int, any) {
		fwdHits.Add(1)
		return 200, map[string]any{
			"ok": true,
			"result": map[string]any{
				"message_id": 77,
				"chat":       map[string]any{"id": 555},
				"date":       1700000000,
			},
		}
	})

	b := newTeledBotForPlatform(t, fp)
	defer b.typing.Stop()

	// SourceMsgID: "<sourceChatJid>|<msgId>"
	id, err := b.Forward(chanlib.ForwardRequest{
		SourceMsgID: "telegram:100|42",
		TargetJID:   "telegram:555",
	})
	if err != nil {
		t.Fatalf("Forward: %v", err)
	}
	if id == "" {
		t.Error("expected non-empty id")
	}
	if fwdHits.Load() != 1 {
		t.Errorf("forwardMessage hits = %d, want 1", fwdHits.Load())
	}
}

// Forward validates the "<sourceChatJid>|<msgId>" shape before touching the
// platform. A missing pipe is the agent's mistake → Unsupported with a hint;
// a malformed id is a hard error. Both must never reach the Telegram API.
func TestBotForward_MalformedSource(t *testing.T) {
	b := &bot{}

	_, err := b.Forward(chanlib.ForwardRequest{SourceMsgID: "telegram:100", TargetJID: "telegram:555"})
	if !errors.Is(err, chanlib.ErrUnsupported) {
		t.Errorf("no-pipe source = %v, want Unsupported", err)
	}

	_, err = b.Forward(chanlib.ForwardRequest{SourceMsgID: "telegram:100|notanum", TargetJID: "telegram:555"})
	if err == nil || errors.Is(err, chanlib.ErrUnsupported) {
		t.Errorf("bad msg id = %v, want a plain error", err)
	}
}
