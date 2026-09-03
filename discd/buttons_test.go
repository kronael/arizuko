package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/bwmarrin/discordgo"
	"github.com/kronael/arizuko/chanlib"
)

// TestSend_DrawsOptionsAsAnActionsRow — discd advertises `buttons`, so chanreg
// hands it the options untouched and discd is the one that draws them.
func TestSend_DrawsOptionsAsAnActionsRow(t *testing.T) {
	var mu sync.Mutex
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		json.NewDecoder(r.Body).Decode(&body)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"msg-1","channel_id":"ch-1"}`))
	}))
	defer srv.Close()
	defer mockDiscordEndpoints(srv.URL)()

	b := &bot{session: newTestSession(t)}
	if _, err := b.Send(chanlib.SendRequest{
		ChatJID: "discord:ch-1", Content: "held for approval",
		Options: []chanlib.Option{
			{Label: "Approve", Data: "/approve ab12"},
			{Label: "Reject", Data: "/reject ab12"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	rows, ok := body["components"].([]any)
	if !ok || len(rows) != 1 {
		t.Fatalf("expected one actions row: %#v", body["components"])
	}
	buttons, ok := rows[0].(map[string]any)["components"].([]any)
	if !ok || len(buttons) != 2 {
		t.Fatalf("expected two buttons: %#v", rows[0])
	}
	first := buttons[0].(map[string]any)
	if first["custom_id"] != "/approve ab12" {
		t.Errorf("custom_id = %v, want the command the press replaces", first["custom_id"])
	}
	if first["label"] != "Approve" {
		t.Errorf("label = %v", first["label"])
	}
}

// TestComponents_OnlyOnTheLastChunkAndCappedAtFive — Discord rejects a row of
// more than five, and a prompt with SOME answers beats a send that fails.
func TestComponents_OnlyOnTheLastChunkAndCappedAtFive(t *testing.T) {
	six := make([]chanlib.Option, 6)
	for i := range six {
		six[i] = chanlib.Option{Label: "o", Data: "d"}
	}
	if got := components(six, false); got != nil {
		t.Error("a non-final chunk carries no buttons")
	}
	if got := components(nil, true); got != nil {
		t.Error("no options means no components")
	}
	row := components(six, true)[0].(discordgo.ActionsRow)
	if len(row.Components) != 5 {
		t.Errorf("row holds %d buttons, want Discord's cap of 5", len(row.Components))
	}
}

// TestOnInteraction_IsAnOrdinaryInboundCommand — a press arrives at routd as the
// text it replaces, from the presser's own id, so it meets whatever gate the
// typed command meets. discd decides nothing.
func TestOnInteraction_IsAnOrdinaryInboundCommand(t *testing.T) {
	var mu sync.Mutex
	var got chanlib.InboundMsg
	router := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		json.NewDecoder(r.Body).Decode(&got)
		mu.Unlock()
		w.Write([]byte(`{"ok":true}`))
	}))
	defer router.Close()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{}`))
	}))
	defer api.Close()
	defer mockDiscordEndpoints(api.URL)()

	s := newTestSession(t)
	addChannel(t, s, "ch-1", discordgo.ChannelTypeGuildText)
	rc := chanlib.NewRouterClient(router.URL)
	rc.SetToken("tok")
	b := &bot{session: s, rc: rc}

	b.onInteraction(s, &discordgo.InteractionCreate{Interaction: &discordgo.Interaction{
		ID:        "int-1",
		Type:      discordgo.InteractionMessageComponent,
		GuildID:   "g1",
		ChannelID: "ch-1",
		Message:   &discordgo.Message{ID: "prompt-7"},
		Member:    &discordgo.Member{User: &discordgo.User{ID: "42", Username: "alice"}},
		Data: discordgo.MessageComponentInteractionData{
			CustomID:      "/approve ab12",
			ComponentType: discordgo.ButtonComponent,
		},
	}})

	mu.Lock()
	defer mu.Unlock()
	if got.Content != "/approve ab12" {
		t.Errorf("content = %q, want the command the button replaces", got.Content)
	}
	if got.Sender != "discord:user/42" {
		t.Errorf("sender = %q, want the presser's own id", got.Sender)
	}
	if got.ChatJID != "discord:g1/ch-1" {
		t.Errorf("chat_jid = %q", got.ChatJID)
	}
	if got.ReplyTo != "prompt-7" {
		t.Errorf("reply_to = %q, want the prompt message", got.ReplyTo)
	}
}

// TestOnInteraction_IgnoresNonComponentInteractions — a slash command or a modal
// is not a button press and must not be routed as chat text.
func TestOnInteraction_IgnoresNonComponentInteractions(t *testing.T) {
	var hits int
	router := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		w.Write([]byte(`{"ok":true}`))
	}))
	defer router.Close()

	rc := chanlib.NewRouterClient(router.URL)
	b := &bot{session: newTestSession(t), rc: rc}
	b.onInteraction(nil, &discordgo.InteractionCreate{Interaction: &discordgo.Interaction{
		Type: discordgo.InteractionApplicationCommand,
	}})
	if hits != 0 {
		t.Errorf("routed %d non-component interactions", hits)
	}
}
