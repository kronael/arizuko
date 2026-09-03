package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/bwmarrin/discordgo"
	"github.com/kronael/arizuko/chanlib"
	"github.com/kronael/arizuko/tests/testutils"
)

// discordAPI records every body posted to the mocked Discord API.
type discordAPI struct {
	mu     sync.Mutex
	bodies []map[string]any
}

func mockDiscordAPI(t *testing.T) *discordAPI {
	t.Helper()
	a := &discordAPI{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		a.mu.Lock()
		a.bodies = append(a.bodies, body)
		a.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"msg-1","channel_id":"ch-1"}`))
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(mockDiscordEndpoints(srv.URL))
	return a
}

func (a *discordAPI) sent() []map[string]any {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.bodies)
}

// press is one Approve press by user 42 on prompt-7 in channelID.
func press(channelID string) *discordgo.InteractionCreate {
	return &discordgo.InteractionCreate{Interaction: &discordgo.Interaction{
		ID:        "int-1",
		Type:      discordgo.InteractionMessageComponent,
		GuildID:   "g1",
		ChannelID: channelID,
		Message:   &discordgo.Message{ID: "prompt-7"},
		Member:    &discordgo.Member{User: &discordgo.User{ID: "42", Username: "alice"}},
		Data: discordgo.MessageComponentInteractionData{
			CustomID:      "/approve ab12",
			ComponentType: discordgo.ButtonComponent,
		},
	}}
}

// TestSend_DrawsOptionsAsAnActionsRow — discd advertises `buttons`, so chanreg
// hands it the options untouched and discd draws them.
func TestSend_DrawsOptionsAsAnActionsRow(t *testing.T) {
	api := mockDiscordAPI(t)
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
	body := api.sent()[0]
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

// TestSend_ButtonsRideTheLastChunk — a long prompt is split; the row sits under
// its end, never after an earlier chunk.
func TestSend_ButtonsRideTheLastChunk(t *testing.T) {
	api := mockDiscordAPI(t)
	b := &bot{session: newTestSession(t)}
	if _, err := b.Send(chanlib.SendRequest{
		ChatJID: "discord:ch-1", Content: strings.Repeat("a", 2500),
		Options: []chanlib.Option{{Label: "Approve", Data: "/approve ab12"}},
	}); err != nil {
		t.Fatal(err)
	}
	sent := api.sent()
	if len(sent) != 2 {
		t.Fatalf("expected two chunks, got %d", len(sent))
	}
	if sent[0]["components"] != nil {
		t.Errorf("first chunk carried buttons: %#v", sent[0]["components"])
	}
	if sent[1]["components"] == nil {
		t.Error("last chunk carries no buttons")
	}
}

// TestButtons_CappedAtFive — Discord allows five buttons in a row, and a prompt
// with SOME answers beats a send that fails.
func TestButtons_CappedAtFive(t *testing.T) {
	if got := buttons(nil); got != nil {
		t.Error("no options means no components")
	}
	six := slices.Repeat([]chanlib.Option{{Label: "o", Data: "d"}}, 6)
	row := buttons(six)[0].(discordgo.ActionsRow)
	if len(row.Components) != 5 {
		t.Errorf("row holds %d buttons, want Discord's cap of 5", len(row.Components))
	}
}

// TestOnPress_IsAnOrdinaryInboundCommand — a press reaches routd as the text it
// replaces, from the presser's own id, placed exactly as a typed message in the
// same channel is. discd decides nothing.
func TestOnPress_IsAnOrdinaryInboundCommand(t *testing.T) {
	mockDiscordAPI(t)
	mr := testutils.NewRouterStub()
	defer mr.Close()
	s := newTestSession(t)
	addChannel(t, s, "ch-1", discordgo.ChannelTypeGuildText)
	rc := chanlib.NewRouterClient(mr.Srv.URL)
	rc.SetToken("tok")
	b := &bot{session: s, rc: rc}

	b.onPress(s, press("ch-1"))

	msgs := mr.Snapshot()
	if len(msgs) != 1 {
		t.Fatalf("expected one inbound message, got %d", len(msgs))
	}
	got := msgs[0]
	if got.Content != "/approve ab12" {
		t.Errorf("content = %q, want the command the button replaces", got.Content)
	}
	if got.Sender != "discord:user/42" {
		t.Errorf("sender = %q, want the presser's own id", got.Sender)
	}
	if got.ChatJID != "discord:g1/ch-1" || got.Topic != "" || !got.IsGroup {
		t.Errorf("chat_jid = %q topic = %q is_group = %v", got.ChatJID, got.Topic, got.IsGroup)
	}
	if got.ReplyTo != "prompt-7" {
		t.Errorf("reply_to = %q, want the prompt message", got.ReplyTo)
	}
}

// TestOnPress_InAThreadIsPlacedLikeATypedMessage — a notice sent into a thread
// is pressed there. The press must land under the parent channel with the
// thread as its topic, where a typed reply in that thread lands; under the
// thread's own id it reaches a chat no route owns.
func TestOnPress_InAThreadIsPlacedLikeATypedMessage(t *testing.T) {
	mockDiscordAPI(t)
	mr := testutils.NewRouterStub()
	defer mr.Close()
	s := newTestSession(t)
	if err := s.State.ChannelAdd(&discordgo.Channel{
		ID: "th-9", GuildID: "g1", Type: discordgo.ChannelTypeGuildPublicThread, ParentID: "ch-1",
	}); err != nil {
		t.Fatal(err)
	}
	rc := chanlib.NewRouterClient(mr.Srv.URL)
	rc.SetToken("tok")
	b := &bot{session: s, rc: rc}

	b.onPress(s, press("th-9"))

	mr.Mu.Lock()
	defer mr.Mu.Unlock()
	if len(mr.Msgs) != 1 {
		t.Fatalf("expected one inbound message, got %d", len(mr.Msgs))
	}
	got := mr.Msgs[0]
	if got.ChatJID != "discord:g1/ch-1" || got.Topic != "th-9" {
		t.Errorf("chat_jid = %q topic = %q, want the parent channel and the thread", got.ChatJID, got.Topic)
	}
}

// TestOnPress_IgnoresNonComponentInteractions — a slash command or a modal is
// not a button press and must not be routed as chat text.
func TestOnPress_IgnoresNonComponentInteractions(t *testing.T) {
	mr := testutils.NewRouterStub()
	defer mr.Close()
	b := &bot{session: newTestSession(t), rc: chanlib.NewRouterClient(mr.Srv.URL)}
	b.onPress(nil, &discordgo.InteractionCreate{Interaction: &discordgo.Interaction{
		Type: discordgo.InteractionApplicationCommand,
	}})
	mr.Mu.Lock()
	defer mr.Mu.Unlock()
	if len(mr.Msgs) != 0 {
		t.Errorf("routed %d non-component interactions", len(mr.Msgs))
	}
}
