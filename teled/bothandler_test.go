package main

import (
	"net/url"
	"regexp"
	"strings"
	"testing"

	tgbotapi "github.com/matterbridge/telegram-bot-api/v6"

	"github.com/onvos/arizuko/chanlib"
	"github.com/onvos/arizuko/tests/testutils"
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
