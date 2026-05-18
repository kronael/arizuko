package main

import (
	"net/url"
	"regexp"
	"strings"
	"testing"

	tgbotapi "github.com/matterbridge/telegram-bot-api/v6"

	"github.com/kronael/arizuko/chanlib"
	"github.com/kronael/arizuko/tests/testutils"
)

// TestBotHandler_Send drives teled's bot.Send against a generic FakePlatform
// stub that accepts a Telegram sendMessage call and returns a valid
// message_id. The test asserts the outbound path and chat_id body field.
func TestBotHandler_Send(t *testing.T) {
	fp := testutils.NewFakePlatform()
	defer fp.Close()

	// Telegram URL shape: /bot<token>/<method>
	fp.On("POST /bot"+testToken+"/sendMessage", func(req testutils.PlatformReq) (int, any) {
		vals, err := url.ParseQuery(string(req.Body))
		if err != nil {
			t.Errorf("parse form: %v", err)
		}
		if vals.Get("chat_id") != "555" {
			t.Errorf("chat_id = %q, want 555", vals.Get("chat_id"))
		}
		if vals.Get("text") != "hello from test" {
			t.Errorf("text = %q", vals.Get("text"))
		}
		return 200, map[string]any{
			"ok": true,
			"result": map[string]any{
				"message_id": 42,
				"chat":       map[string]any{"id": 555},
				"date":       1700000000,
			},
		}
	})
	// getMe is called by NewBotAPIWithAPIEndpoint.
	fp.On("POST /bot"+testToken+"/getMe", func(testutils.PlatformReq) (int, any) {
		return 200, map[string]any{"ok": true, "result": map[string]any{
			"id": 1, "is_bot": true, "username": "TestBot", "first_name": "Test",
		}}
	})

	b := newTeledBotForPlatform(t, fp)
	defer b.typing.Stop()

	id, err := b.Send(chanlib.SendRequest{ChatJID: "telegram:555", Content: "hello from test"})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if id == "" {
		t.Error("expected non-empty id")
	}

	// At least one POST /bot<token>/sendMessage recorded.
	var found bool
	for _, r := range fp.Requests() {
		if r.Method == "POST" && strings.HasSuffix(r.Path, "/sendMessage") {
			found = true
		}
	}
	if !found {
		t.Errorf("no sendMessage recorded; saw %+v", fp.Requests())
	}
}

const testToken = "tok"

// newTeledBotForPlatform builds a bot wired to a FakePlatform by pointing
// tgbotapi at its URL. The platform must already have handlers registered
// for getMe (constructor call) and any method the test exercises.
func newTeledBotForPlatform(t *testing.T, fp *testutils.FakePlatform) *bot {
	t.Helper()
	endpoint := fp.URL() + "/bot%s/%s"
	api, err := tgbotapi.NewBotAPIWithAPIEndpoint(testToken, endpoint)
	if err != nil {
		t.Fatalf("NewBotAPI: %v", err)
	}
	cfg := config{Name: "telegram", AssistantName: "Ari"}
	b := &bot{api: api, cfg: cfg, done: make(chan struct{})}
	b.typing = chanlib.NewTypingRefresher(50_000_000, 1_000_000_000, b.sendTyping, nil)
	b.mentionRe = regexp.MustCompile(`(?i)^@Ari\b`)
	return b
}

func TestBotHandler_UnsupportedHints_Teled(t *testing.T) {
	b := &bot{}
	if _, err := b.Quote(chanlib.QuoteRequest{}); !teledHasHint(err) {
		t.Errorf("quote: missing hint err=%v", err)
	}
	if _, err := b.Repost(chanlib.RepostRequest{}); !teledHasHint(err) {
		t.Errorf("repost: missing hint err=%v", err)
	}
}

func TestBotHandler_Like_Teled(t *testing.T) {
	fp := testutils.NewFakePlatform()
	defer fp.Close()
	fp.On("POST /bot"+testToken+"/getMe", func(testutils.PlatformReq) (int, any) {
		return 200, map[string]any{"ok": true, "result": map[string]any{
			"id": 1, "is_bot": true, "username": "TestBot", "first_name": "Test",
		}}
	})
	var seen []url.Values
	fp.On("POST /bot"+testToken+"/setMessageReaction", func(req testutils.PlatformReq) (int, any) {
		v, _ := url.ParseQuery(string(req.Body))
		seen = append(seen, v)
		return 200, map[string]any{"ok": true, "result": true}
	})
	b := newTeledBotForPlatform(t, fp)
	defer b.typing.Stop()

	if err := b.Like(chanlib.LikeRequest{ChatJID: "telegram:555", TargetID: "42"}); err != nil {
		t.Fatalf("Like: %v", err)
	}
	if len(seen) != 1 {
		t.Fatalf("setMessageReaction calls = %d, want 1", len(seen))
	}
	if !strings.Contains(seen[0].Get("reaction"), "👍") {
		t.Errorf("Like reaction = %q, want 👍", seen[0].Get("reaction"))
	}
	if seen[0].Get("chat_id") != "555" || seen[0].Get("message_id") != "42" {
		t.Errorf("chat_id/message_id wrong: %v", seen[0])
	}
}

func TestBotHandler_DislikeHint_Teled(t *testing.T) {
	b := &bot{}
	err := b.Dislike(chanlib.DislikeRequest{})
	ue, ok := err.(*chanlib.UnsupportedError)
	if !ok || ue.Hint == "" {
		t.Fatalf("dislike: want *UnsupportedError with hint, got %v", err)
	}
	if !strings.Contains(ue.Hint, "like") || !strings.Contains(ue.Hint, "👎") {
		t.Errorf("dislike hint missing like/👎: %q", ue.Hint)
	}
}

func teledHasHint(err error) bool {
	if err == nil {
		return false
	}
	ue, ok := err.(*chanlib.UnsupportedError)
	return ok && ue.Hint != ""
}

// Edit/Delete dispatch via b.do → MakeRequest; both round-trip through
// the same FakePlatform stub. Verifies they reach the right Telegram
// endpoints and forward chat_id/message_id parameters correctly.
func TestBotHandler_Edit_Teled(t *testing.T) {
	fp := testutils.NewFakePlatform()
	defer fp.Close()
	fp.On("POST /bot"+testToken+"/getMe", func(testutils.PlatformReq) (int, any) {
		return 200, map[string]any{"ok": true, "result": map[string]any{
			"id": 1, "is_bot": true, "username": "TestBot", "first_name": "Test",
		}}
	})
	var seen url.Values
	fp.On("POST /bot"+testToken+"/editMessageText", func(req testutils.PlatformReq) (int, any) {
		seen, _ = url.ParseQuery(string(req.Body))
		return 200, map[string]any{"ok": true, "result": map[string]any{
			"message_id": 42, "chat": map[string]any{"id": 555}, "date": 1,
		}}
	})
	b := newTeledBotForPlatform(t, fp)
	defer b.typing.Stop()

	if err := b.Edit(chanlib.EditRequest{
		ChatJID: "telegram:555", TargetID: "42", Content: "updated",
	}); err != nil {
		t.Fatalf("Edit: %v", err)
	}
	if seen.Get("chat_id") != "555" || seen.Get("message_id") != "42" {
		t.Errorf("ids = %v", seen)
	}
	if seen.Get("text") != "updated" {
		t.Errorf("text = %q", seen.Get("text"))
	}
}

func TestBotHandler_Delete_Teled(t *testing.T) {
	fp := testutils.NewFakePlatform()
	defer fp.Close()
	fp.On("POST /bot"+testToken+"/getMe", func(testutils.PlatformReq) (int, any) {
		return 200, map[string]any{"ok": true, "result": map[string]any{
			"id": 1, "is_bot": true, "username": "TestBot", "first_name": "Test",
		}}
	})
	var seen url.Values
	fp.On("POST /bot"+testToken+"/deleteMessage", func(req testutils.PlatformReq) (int, any) {
		seen, _ = url.ParseQuery(string(req.Body))
		return 200, map[string]any{"ok": true, "result": true}
	})
	b := newTeledBotForPlatform(t, fp)
	defer b.typing.Stop()

	if err := b.Delete(chanlib.DeleteRequest{
		ChatJID: "telegram:555", TargetID: "42",
	}); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if seen.Get("chat_id") != "555" || seen.Get("message_id") != "42" {
		t.Errorf("ids = %v", seen)
	}
}

// Forward requires source_msg_id in "<sourceChatJid>|<msgId>" form; the
// hint-only error path fires for malformed inputs.
func TestBotHandler_Forward_BadFormat_Teled(t *testing.T) {
	b := &bot{}
	_, err := b.Forward(chanlib.ForwardRequest{
		SourceMsgID: "no-pipe-here", TargetJID: "telegram:555",
	})
	if !teledHasHint(err) {
		t.Errorf("forward without pipe should return UnsupportedError hint, got %v", err)
	}
}
