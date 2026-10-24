package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	tgbotapi "github.com/matterbridge/telegram-bot-api/v6"

	"github.com/kronael/arizuko/chanlib"
	"github.com/kronael/arizuko/tests/testutils"
)

// TestBotSend_DrawsOptionsAsAnInlineKeyboard — teled advertises `buttons`, so
// chanreg hands it the options untouched and teled draws them.
func TestBotSend_DrawsOptionsAsAnInlineKeyboard(t *testing.T) {
	m := newTGMock()
	defer m.close()
	b := newTestBot(t, m, config{Name: "telegram"})
	defer b.typing.Stop()

	if _, err := b.Send(chanlib.SendRequest{
		ChatJID: "telegram:1/user/123",
		Content: "held for approval",
		Options: []chanlib.Option{
			{Label: "✅ Approve", Data: "/approve ab12"},
			{Label: "⛔ Reject", Data: "/reject ab12"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.lastSent) != 1 {
		t.Fatalf("expected one send, got %d", len(m.lastSent))
	}
	markup, _ := m.lastSent[0]["reply_markup"].(string)
	for _, want := range []string{"inline_keyboard", "/approve ab12", "/reject ab12", "✅ Approve"} {
		if !strings.Contains(markup, want) {
			t.Errorf("reply_markup missing %q:\n%s", want, markup)
		}
	}
}

// TestBotSend_ButtonsRideTheLastChunk — a long prompt is split; the keyboard
// sits under its end, never after an earlier chunk.
func TestBotSend_ButtonsRideTheLastChunk(t *testing.T) {
	m := newTGMock()
	defer m.close()
	b := newTestBot(t, m, config{Name: "telegram"})
	defer b.typing.Stop()

	if _, err := b.Send(chanlib.SendRequest{
		ChatJID: "telegram:1/user/123",
		Content: strings.Repeat("a", 5000),
		Options: []chanlib.Option{{Label: "Approve", Data: "/approve ab12"}},
	}); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.lastSent) != 2 {
		t.Fatalf("expected two chunks, got %d", len(m.lastSent))
	}
	if markup, _ := m.lastSent[0]["reply_markup"].(string); markup != "" {
		t.Errorf("first chunk carried the keyboard: %s", markup)
	}
	if markup, _ := m.lastSent[1]["reply_markup"].(string); !strings.Contains(markup, "/approve ab12") {
		t.Errorf("last chunk carries no keyboard: %s", markup)
	}
}

// TestBotSend_OptionsShareOneRow — a verdict is a choice BETWEEN the answers,
// so they sit side by side. One row per option reads as a list of separate
// actions, which is what it looked like before.
func TestBotSend_OptionsShareOneRow(t *testing.T) {
	m := newTGMock()
	defer m.close()
	b := newTestBot(t, m, config{Name: "telegram"})
	defer b.typing.Stop()

	if _, err := b.Send(chanlib.SendRequest{
		ChatJID: "telegram:1/user/123", Content: "held",
		Options: []chanlib.Option{
			{Label: "Approve", Data: "/approve ab12"},
			{Label: "Reject", Data: "/reject ab12"},
			{Label: "Always", Data: "/always ab12"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	markup, _ := m.lastSent[0]["reply_markup"].(string)
	var kb struct {
		Rows [][]struct {
			CallbackData string `json:"callback_data"`
		} `json:"inline_keyboard"`
	}
	if err := json.Unmarshal([]byte(markup), &kb); err != nil {
		t.Fatalf("reply_markup is not a keyboard: %v (%s)", err, markup)
	}
	if len(kb.Rows) != 1 {
		t.Fatalf("keyboard has %d rows, want the answers side by side in 1", len(kb.Rows))
	}
	if len(kb.Rows[0]) != 3 {
		t.Fatalf("row holds %d buttons, want all 3", len(kb.Rows[0]))
	}
}

// TestBotSend_NoOptionsNoKeyboard — an ordinary message must not grow a markup
// field, which would make every reply look like a prompt.
func TestBotSend_NoOptionsNoKeyboard(t *testing.T) {
	m := newTGMock()
	defer m.close()
	b := newTestBot(t, m, config{Name: "telegram"})
	defer b.typing.Stop()

	if _, err := b.Send(chanlib.SendRequest{ChatJID: "telegram:1/user/123", Content: "hello"}); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if markup, _ := m.lastSent[0]["reply_markup"].(string); markup != "" {
		t.Errorf("plain send carried a keyboard: %s", markup)
	}
}

// TestHandlePress_IsAnOrdinaryInboundCommand — a press reaches routd as the
// text it replaces, from the presser's own id, placed exactly as a typed message
// in the same chat and topic is. teled decides nothing.
func TestHandlePress_IsAnOrdinaryInboundCommand(t *testing.T) {
	m := newTGMock()
	defer m.close()
	mr := testutils.NewRouterStub()
	defer mr.Close()
	rc := chanlib.NewRouterClient(mr.Srv.URL)
	rc.SetToken("tok")

	b := newTestBot(t, m, config{Name: "telegram"})
	defer b.typing.Stop()

	q := &tgbotapi.CallbackQuery{
		ID:   "cbq1",
		From: &tgbotapi.User{ID: 42, FirstName: "Alice"},
		Data: "/approve ab12",
		Message: &tgbotapi.Message{
			MessageID: 7, MessageThreadID: 55,
			Chat: &tgbotapi.Chat{ID: -100123, Title: "ops"},
		},
	}
	if !b.handlePress(q, rc) {
		t.Fatal("handlePress returned false")
	}
	msgs := mr.Snapshot()
	if len(msgs) != 1 {
		t.Fatalf("expected one inbound message, got %d", len(msgs))
	}
	got := msgs[0]
	if got.Content != "/approve ab12" {
		t.Errorf("content = %q, want the command the button replaces", got.Content)
	}
	if got.Sender != "telegram:user/42" {
		t.Errorf("sender = %q, want the presser's own id", got.Sender)
	}
	if got.ChatJID != "telegram:group/100123" || got.Topic != "55" || got.ChatName != "ops" {
		t.Errorf("chat_jid = %q topic = %q chat_name = %q", got.ChatJID, got.Topic, got.ChatName)
	}
	if got.ReplyTo != "telegram:group/100123/7" {
		t.Errorf("reply_to = %q, want the prompt message", got.ReplyTo)
	}
	if time.Since(time.Unix(got.Timestamp, 0)) > time.Minute {
		t.Errorf("timestamp = %d, want now", got.Timestamp)
	}
}

// TestHandlePress_EmptyPressAdvancesTheOffset — a press with nothing to route
// must still be consumed. Returning false would stall Telegram's offset and
// re-deliver the same update forever.
func TestHandlePress_EmptyPressAdvancesTheOffset(t *testing.T) {
	m := newTGMock()
	defer m.close()
	mr := testutils.NewRouterStub()
	defer mr.Close()
	rc := chanlib.NewRouterClient(mr.Srv.URL)
	rc.SetToken("tok")

	b := newTestBot(t, m, config{Name: "telegram"})
	defer b.typing.Stop()

	if !b.handlePress(&tgbotapi.CallbackQuery{ID: "cbq2"}, rc) {
		t.Fatal("a press with no data must not stall the offset")
	}
	mr.Mu.Lock()
	defer mr.Mu.Unlock()
	if len(mr.Msgs) != 0 {
		t.Fatalf("nothing to route, but %d messages were sent", len(mr.Msgs))
	}
}
