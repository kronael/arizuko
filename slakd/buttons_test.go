package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/kronael/arizuko/chanlib"
)

// TestActionBlocks_RendersOneActionsBlock — a button's `value` is the command it
// replaces, which is what makes the press and the typed command one path with
// one operator gate. `text` stays as the notification fallback.
func TestActionBlocks_RendersOneActionsBlock(t *testing.T) {
	raw := actionBlocks("held for approval", []chanlib.Option{
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

// TestActionBlocks_CappedAtFiveAndEmptyWithoutOptions — Slack rejects an actions
// block of more than five elements, and an ordinary message must not grow blocks.
func TestActionBlocks_CappedAtFiveAndEmptyWithoutOptions(t *testing.T) {
	if got := actionBlocks("hi", nil); got != "" {
		t.Errorf("no options must render no blocks, got %s", got)
	}
	six := make([]chanlib.Option, 6)
	for i := range six {
		six[i] = chanlib.Option{Label: "o", Data: "d"}
	}
	var blocks []map[string]any
	json.Unmarshal([]byte(actionBlocks("hi", six)), &blocks)
	if els := blocks[1]["elements"].([]any); len(els) != 5 {
		t.Errorf("block holds %d elements, want Slack's cap of 5", len(els))
	}
}

// TestSend_PostsBlocksWithTheOptions — the wire check: chat.postMessage carries
// the blocks, and the plain text survives as the fallback.
func TestSend_PostsBlocksWithTheOptions(t *testing.T) {
	var mu sync.Mutex
	var form string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		mu.Lock()
		form = r.Form.Encode()
		mu.Unlock()
		w.Write([]byte(`{"ok":true,"ts":"1.1"}`))
	}))
	defer srv.Close()

	b, err := newBotWithBase(config{Name: "slack", BotToken: "x"}, srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Send(chanlib.SendRequest{
		ChatJID: "slack:T1/channel/C1", Content: "held",
		Options: []chanlib.Option{{Label: "Approve", Data: "/approve ab12"}},
	}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, want := range []string{"blocks=", "%2Fapprove+ab12", "text=held"} {
		if !strings.Contains(form, want) {
			t.Errorf("postMessage form missing %q:\n%s", want, form)
		}
	}
}

// TestHandleInteraction_IsAnOrdinaryInboundCommand — a press arrives at routd as
// the text it replaces, from the presser's own id, so it meets whatever gate the
// typed command meets.
func TestHandleInteraction_IsAnOrdinaryInboundCommand(t *testing.T) {
	var mu sync.Mutex
	var got chanlib.InboundMsg
	router := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		json.NewDecoder(r.Body).Decode(&got)
		mu.Unlock()
		w.Write([]byte(`{"ok":true}`))
	}))
	defer router.Close()
	slack := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"ok":false,"error":"channel_not_found"}`))
	}))
	defer slack.Close()

	b, err := newBotWithBase(config{Name: "slack", BotToken: "x"}, slack.URL)
	if err != nil {
		t.Fatal(err)
	}
	b.rc = chanlib.NewRouterClient(router.URL)
	b.rc.SetToken("tok")

	b.handleInteraction([]byte(`{
	  "type":"block_actions",
	  "user":{"id":"U42","username":"alice"},
	  "team":{"id":"T1"},
	  "channel":{"id":"C1"},
	  "message":{"ts":"1700.7"},
	  "actions":[{"action_id":"opt0","value":"/approve ab12"}]
	}`))

	mu.Lock()
	defer mu.Unlock()
	if got.Content != "/approve ab12" {
		t.Errorf("content = %q, want the command the button replaces", got.Content)
	}
	if got.Sender != "slack:user/U42" {
		t.Errorf("sender = %q, want the presser's own id", got.Sender)
	}
	if got.ReplyTo != "1700.7" {
		t.Errorf("reply_to = %q, want the prompt message", got.ReplyTo)
	}
	if !strings.HasPrefix(got.ChatJID, "slack:T1/") {
		t.Errorf("chat_jid = %q", got.ChatJID)
	}
}

// TestHandleInteraction_IgnoresEverythingElse — a view submission or an empty
// action is not a button press and must not be routed as chat text.
func TestHandleInteraction_IgnoresEverythingElse(t *testing.T) {
	var hits int
	router := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		w.Write([]byte(`{"ok":true}`))
	}))
	defer router.Close()

	b := &bot{rc: chanlib.NewRouterClient(router.URL)}
	for _, payload := range []string{
		`{"type":"view_submission"}`,
		`{"type":"block_actions","actions":[]}`,
		`{"type":"block_actions","actions":[{"action_id":"opt0","value":""}]}`,
		`not json`,
	} {
		b.handleInteraction([]byte(payload))
	}
	if hits != 0 {
		t.Errorf("routed %d non-press payloads", hits)
	}
}
