package main

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/kronael/arizuko/chanlib"
)

// TestButtons_RendersOneActionsBlock — a button's `value` is the command it
// replaces; `text` stays as the notification fallback.
func TestButtons_RendersOneActionsBlock(t *testing.T) {
	raw := buttons("held for approval", []chanlib.Option{
		{Label: "Approve", Data: "/approve ab12"},
		{Label: "Reject", Data: "/reject ab12"},
	})
	var blocks []map[string]any
	if err := json.Unmarshal([]byte(raw), &blocks); err != nil {
		t.Fatalf("blocks are not JSON: %v (%s)", err, raw)
	}
	if len(blocks) != 2 || blocks[0]["type"] != "section" || blocks[1]["type"] != "actions" {
		t.Fatalf("want section+actions, got %s", raw)
	}
	els := blocks[1]["elements"].([]any)
	if len(els) != 2 {
		t.Fatalf("want two buttons, got %d", len(els))
	}
	first := els[0].(map[string]any)
	if first["value"] != "/approve ab12" {
		t.Errorf("value = %v, want the command the press replaces", first["value"])
	}
	if first["action_id"] == els[1].(map[string]any)["action_id"] {
		t.Error("action_id must be unique within the block")
	}
}

// TestButtons_CappedAt25AndEmptyWithoutOptions — Slack allows 25 elements in an
// actions block, and an ordinary message must not grow blocks.
func TestButtons_CappedAt25AndEmptyWithoutOptions(t *testing.T) {
	if got := buttons("hi", nil); got != "" {
		t.Errorf("no options must render no blocks, got %s", got)
	}
	many := slices.Repeat([]chanlib.Option{{Label: "o", Data: "d"}}, 26)
	var blocks []map[string]any
	json.Unmarshal([]byte(buttons("hi", many)), &blocks)
	if els := blocks[1]["elements"].([]any); len(els) != 25 {
		t.Errorf("block holds %d elements, want Slack's cap of 25", len(els))
	}
}

// TestSend_PostsBlocksWithTheOptions — the wire check: chat.postMessage carries
// the blocks, and the plain text survives as the fallback.
func TestSend_PostsBlocksWithTheOptions(t *testing.T) {
	mock := newSlackMock()
	defer mock.Close()
	b, _ := setupBot(t, mock)
	if _, err := b.Send(chanlib.SendRequest{
		ChatJID: "slack:T012/channel/C1", Content: "held",
		Options: []chanlib.Option{{Label: "Approve", Data: "/approve ab12"}},
	}); err != nil {
		t.Fatal(err)
	}
	mock.mu.Lock()
	defer mock.mu.Unlock()
	post := mock.posted[0]
	if post["text"] != "held" || !strings.Contains(post["blocks"], "/approve ab12") {
		t.Errorf("postMessage text=%q blocks=%s", post["text"], post["blocks"])
	}
}

// TestHandlePress_IsAnOrdinaryInboundCommand — a press reaches routd as the
// text it replaces, from the presser's own id, placed exactly as a typed reply
// in the same thread is. slakd decides nothing.
func TestHandlePress_IsAnOrdinaryInboundCommand(t *testing.T) {
	mock := newSlackMock()
	defer mock.Close()
	b, rm := setupBot(t, mock)

	b.handlePress([]byte(`{
	  "type":"block_actions",
	  "trigger_id":"trg1",
	  "user":{"id":"U42","username":"alice"},
	  "team":{"id":"T012"},
	  "channel":{"id":"C1"},
	  "message":{"ts":"1700.7","thread_ts":"1700.1"},
	  "actions":[{"action_id":"opt0","value":"/approve ab12"}]
	}`))

	msgs := rm.Snapshot()
	if len(msgs) != 1 {
		t.Fatalf("expected one inbound message, got %d", len(msgs))
	}
	got := msgs[0]
	if got.Content != "/approve ab12" {
		t.Errorf("content = %q, want the command the button replaces", got.Content)
	}
	if got.Sender != "slack:user/U42" {
		t.Errorf("sender = %q, want the presser's own id", got.Sender)
	}
	if got.ChatJID != "slack:T012/channel/C1" || got.Topic != "1700.1" || got.ReplyTo != "1700.7" {
		t.Errorf("chat_jid = %q topic = %q reply_to = %q", got.ChatJID, got.Topic, got.ReplyTo)
	}
	if got.ID != "slack:T012/channel/C1/1700.7:cb:trg1" {
		t.Errorf("id = %q, want one per press so two presses of one button both reach routd", got.ID)
	}
}

// TestHandlePress_IgnoresEverythingElse — a view submission or an empty action
// is not a button press and must not be routed as chat text.
func TestHandlePress_IgnoresEverythingElse(t *testing.T) {
	mock := newSlackMock()
	defer mock.Close()
	b, rm := setupBot(t, mock)
	for _, payload := range []string{
		`{"type":"view_submission"}`,
		`{"type":"block_actions","actions":[]}`,
		`{"type":"block_actions","actions":[{"action_id":"opt0","value":""}]}`,
		`not json`,
	} {
		b.handlePress([]byte(payload))
	}
	if n := len(rm.Snapshot()); n != 0 {
		t.Errorf("routed %d non-press payloads", n)
	}
}
