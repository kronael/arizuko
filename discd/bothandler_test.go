package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/bwmarrin/discordgo"

	"github.com/onvos/arizuko/chanlib"
)

// mockAllDiscordEndpoints overrides every discordgo endpoint used by the
// BotHandler surface so they route to a single test server. Returns a
// cleanup that restores the originals.
func mockAllDiscordEndpoints(base string) func() {
	oldMsgs := discordgo.EndpointChannelMessages
	oldTyping := discordgo.EndpointChannelTyping
	oldMsg := discordgo.EndpointChannelMessage
	oldMsgReaction := discordgo.EndpointMessageReaction

	discordgo.EndpointChannelMessages = func(cID string) string {
		return base + "/channels/" + cID + "/messages"
	}
	discordgo.EndpointChannelTyping = func(cID string) string {
		return base + "/channels/" + cID + "/typing"
	}
	discordgo.EndpointChannelMessage = func(cID, mID string) string {
		return base + "/channels/" + cID + "/messages/" + mID
	}
	discordgo.EndpointMessageReaction = func(cID, mID, eID, uID string) string {
		return base + "/channels/" + cID + "/messages/" + mID + "/reactions/" + eID + "/" + uID
	}
	return func() {
		discordgo.EndpointChannelMessages = oldMsgs
		discordgo.EndpointChannelTyping = oldTyping
		discordgo.EndpointChannelMessage = oldMsg
		discordgo.EndpointMessageReaction = oldMsgReaction
	}
}

type recorded struct {
	sync.Mutex
	reqs []struct {
		Method, Path, Body string
	}
}

func (r *recorded) record(method, path, body string) {
	r.Lock()
	defer r.Unlock()
	r.reqs = append(r.reqs, struct{ Method, Path, Body string }{method, path, body})
}

func (r *recorded) snapshot() []struct{ Method, Path, Body string } {
	r.Lock()
	defer r.Unlock()
	cp := make([]struct{ Method, Path, Body string }, len(r.reqs))
	copy(cp, r.reqs)
	return cp
}

func newRecordingDiscordServer(t *testing.T, rec *recorded) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var buf [4096]byte
		n, _ := r.Body.Read(buf[:])
		rec.record(r.Method, r.URL.Path, string(buf[:n]))
		w.Header().Set("Content-Type", "application/json")
		// Minimal response: messages endpoints return an id; DELETE returns 204.
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if strings.Contains(r.URL.Path, "/reactions/") {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Write([]byte(`{"id":"msg-ok","channel_id":"ch-1"}`))
	}))
}

func TestBotHandler_Send(t *testing.T) {
	rec := &recorded{}
	srv := newRecordingDiscordServer(t, rec)
	defer srv.Close()
	defer mockAllDiscordEndpoints(srv.URL)()

	b := &bot{session: newTestSession(t)}
	id, err := b.Send(chanlib.SendRequest{ChatJID: "discord:ch-1", Content: "hi"})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if id != "msg-ok" {
		t.Errorf("id = %q", id)
	}
	assertPath(t, rec, "POST", "/channels/ch-1/messages")
}

func TestBotHandler_Like(t *testing.T) {
	rec := &recorded{}
	srv := newRecordingDiscordServer(t, rec)
	defer srv.Close()
	defer mockAllDiscordEndpoints(srv.URL)()

	b := &bot{session: newTestSession(t)}
	if err := b.Like(chanlib.LikeRequest{
		ChatJID: "discord:ch-1", TargetID: "msg-99", Reaction: "🔥",
	}); err != nil {
		t.Fatalf("Like: %v", err)
	}
	// URL is /channels/<ch>/messages/<mid>/reactions/<emoji>/<user>
	var ok bool
	for _, r := range rec.snapshot() {
		if r.Method == "PUT" && strings.Contains(r.Path, "/channels/ch-1/messages/msg-99/reactions/") {
			ok = true
		}
	}
	if !ok {
		t.Errorf("no reaction PUT recorded: %+v", rec.snapshot())
	}
}

func TestBotHandler_Post(t *testing.T) {
	rec := &recorded{}
	srv := newRecordingDiscordServer(t, rec)
	defer srv.Close()
	defer mockAllDiscordEndpoints(srv.URL)()

	b := &bot{session: newTestSession(t)}
	id, err := b.Post(chanlib.PostRequest{ChatJID: "discord:ch-1", Content: "new post"})
	if err != nil {
		t.Fatalf("Post: %v", err)
	}
	if id != "msg-ok" {
		t.Errorf("id = %q", id)
	}
	// Look for the body containing the content.
	found := false
	for _, r := range rec.snapshot() {
		if r.Method == "POST" && strings.HasSuffix(r.Path, "/messages") {
			var body map[string]any
			json.Unmarshal([]byte(r.Body), &body)
			if body["content"] == "new post" {
				found = true
			}
		}
	}
	if !found {
		t.Errorf("post body not captured: %+v", rec.snapshot())
	}
}

func TestBotHandler_Delete(t *testing.T) {
	rec := &recorded{}
	srv := newRecordingDiscordServer(t, rec)
	defer srv.Close()
	defer mockAllDiscordEndpoints(srv.URL)()

	b := &bot{session: newTestSession(t)}
	if err := b.Delete(chanlib.DeleteRequest{
		ChatJID: "discord:ch-1", TargetID: "msg-99",
	}); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	assertPath(t, rec, "DELETE", "/channels/ch-1/messages/msg-99")
}

func assertPath(t *testing.T, rec *recorded, method, path string) {
	t.Helper()
	for _, r := range rec.snapshot() {
		if r.Method == method && r.Path == path {
			return
		}
	}
	t.Errorf("no %s %s recorded; saw %+v", method, path, rec.snapshot())
}

// TestBotHandler_UnsupportedHints asserts the new verbs that aren't
// natively supported on Discord return *chanlib.UnsupportedError with a
// hint referencing a concrete alternative tool.
func TestBotHandler_UnsupportedHints(t *testing.T) {
	b := &bot{session: newTestSession(t)}
	cases := []struct {
		name string
		err  error
	}{
		{"forward", mustErr(b.Forward(chanlib.ForwardRequest{}))},
		{"quote", mustErr(b.Quote(chanlib.QuoteRequest{}))},
		{"repost", mustErr(b.Repost(chanlib.RepostRequest{}))},
		{"dislike", b.Dislike(chanlib.DislikeRequest{})},
	}
	for _, c := range cases {
		var ue *chanlib.UnsupportedError
		if !errors.As(c.err, &ue) {
			t.Errorf("%s: want *UnsupportedError, got %v", c.name, c.err)
			continue
		}
		if ue.Hint == "" {
			t.Errorf("%s: empty hint", c.name)
		}
	}
	// Dislike hint must redirect to like(emoji="👎").
	err := b.Dislike(chanlib.DislikeRequest{})
	var ue *chanlib.UnsupportedError
	if !errors.As(err, &ue) {
		t.Fatalf("dislike: want *UnsupportedError, got %v", err)
	}
	if !strings.Contains(ue.Hint, "like") || !strings.Contains(ue.Hint, "👎") {
		t.Errorf("dislike hint missing like/👎: %q", ue.Hint)
	}
}

func mustErr(_ string, e error) error { return e }
