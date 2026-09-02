package main

import (
	"strings"
	"testing"
	"time"

	tgbotapi "github.com/matterbridge/telegram-bot-api/v6"

	"github.com/kronael/arizuko/chanlib"
)

// TestBotSend_DrawsOptionsAsAnInlineKeyboard — teled advertises `buttons`, so
// chanreg hands it the options untouched and teled is the one that draws them.
func TestBotSend_DrawsOptionsAsAnInlineKeyboard(t *testing.T) {
	m := newTGMock()
	defer m.close()
	b := newTestBot(t, m, config{Name: "telegram"})
	defer b.typing.Stop()

	if _, err := b.Send(chanlib.SendRequest{
		ChatJID: "telegram:123",
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

// TestBotSend_NoOptionsNoKeyboard — an ordinary message must not grow a markup
// field, which would make every reply look like a prompt.
func TestBotSend_NoOptionsNoKeyboard(t *testing.T) {
	m := newTGMock()
	defer m.close()
	b := newTestBot(t, m, config{Name: "telegram"})
	defer b.typing.Stop()

	if _, err := b.Send(chanlib.SendRequest{ChatJID: "telegram:123", Content: "hello"}); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if markup, _ := m.lastSent[0]["reply_markup"].(string); markup != "" {
		t.Errorf("plain send carried a keyboard: %s", markup)
	}
}

// TestHandleCallback_IsAnOrdinaryInboundCommand is the whole point of the button
// design: a press arrives at routd as the text it replaces, from the PRESSER's
// own id. teled decides nothing — the reserved-command parser and its operator
// gate run exactly as they do for a typed `/approve`.
func TestHandleCallback_IsAnOrdinaryInboundCommand(t *testing.T) {
	m := newTGMock()
	defer m.close()
	mr := newTeledRouterMock()
	defer mr.close()
	rc := chanlib.NewRouterClient(mr.srv.URL)
	rc.SetToken("tok")

	b := newTestBot(t, m, config{Name: "telegram"})
	defer b.typing.Stop()

	q := &callbackQuery{ID: "cbq1", From: &tgbotapi.User{ID: 42, FirstName: "Alice"}, Data: "/approve ab12"}
	q.Message = &struct {
		MessageID int `json:"message_id"`
		Chat      struct {
			ID int64 `json:"id"`
		} `json:"chat"`
	}{MessageID: 7}
	q.Message.Chat.ID = -100123

	if !b.handleCallback(q, rc) {
		t.Fatal("handleCallback returned false")
	}
	mr.mu.Lock()
	defer mr.mu.Unlock()
	if len(mr.msgs) != 1 {
		t.Fatalf("expected one inbound message, got %d", len(mr.msgs))
	}
	got := mr.msgs[0]
	if got.Content != "/approve ab12" {
		t.Errorf("content = %q, want the command the button replaces", got.Content)
	}
	if got.Sender != "telegram:user/42" {
		t.Errorf("sender = %q, want the presser's own id", got.Sender)
	}
	if got.ChatJID != "telegram:group/100123" {
		t.Errorf("chat_jid = %q", got.ChatJID)
	}
	if got.ReplyTo != "telegram:group/100123/7" {
		t.Errorf("reply_to = %q, want the prompt message", got.ReplyTo)
	}
	if time.Since(time.Unix(got.Timestamp, 0)) > time.Minute {
		t.Errorf("timestamp = %d, want now", got.Timestamp)
	}
}

// TestHandleCallback_EmptyPressAdvancesTheOffset — a press with nothing to route
// must still be consumed. Returning false would stall Telegram's offset and
// re-deliver the same update forever.
func TestHandleCallback_EmptyPressAdvancesTheOffset(t *testing.T) {
	m := newTGMock()
	defer m.close()
	mr := newTeledRouterMock()
	defer mr.close()
	rc := chanlib.NewRouterClient(mr.srv.URL)
	rc.SetToken("tok")

	b := newTestBot(t, m, config{Name: "telegram"})
	defer b.typing.Stop()

	if !b.handleCallback(&callbackQuery{ID: "cbq2"}, rc) {
		t.Fatal("a press with no data must not stall the offset")
	}
	mr.mu.Lock()
	defer mr.mu.Unlock()
	if len(mr.msgs) != 0 {
		t.Fatalf("nothing to route, but %d messages were sent", len(mr.msgs))
	}
}
