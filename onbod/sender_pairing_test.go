package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/core"
)

func TestPairingDiscordDMResolvesSender(t *testing.T) {
	xdb, obdb := migratedDB(t)
	const chat = "discord:dm/456"
	const sender = "discord:user/123"
	const sub = "github:alice"
	if err := xdb.PutMessage(core.Message{
		ID: "first", ChatJID: chat, Sender: sender,
		Content: "hello", Timestamp: time.Now(), Verb: "message",
	}); err != nil {
		t.Fatal(err)
	}
	seedGrant(t, xdb, sub, "admin", "alice")

	mux := http.NewServeMux()
	(&admin{st: obdb}).mountOnboarding(mux)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("POST", "/v1/onboarding",
		strings.NewReader(`{"jid":"discord:dm/456","sender":"discord:user/123"}`)))
	if w.Code != http.StatusOK {
		t.Fatalf("onboarding: %d %s", w.Code, w.Body.String())
	}

	deliveries := make(chan map[string]string, 2)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		deliveries <- body
	}))
	t.Cleanup(srv.Close)
	cfg := config{gatedURL: srv.URL, authBaseURL: "https://example.test",
		svcToken: func(context.Context) (string, error) { return "service", nil }}
	promptUnprompted(xdb, obdb, cfg)
	var delivery map[string]string
	select {
	case delivery = <-deliveries:
	default:
		t.Fatal("no pairing link delivered")
	}
	if delivery["jid"] != chat {
		t.Fatalf("link delivered to %q, want chat %q", delivery["jid"], chat)
	}
	raw := strings.TrimPrefix(delivery["text"],
		"Set up your account: https://example.test/pair/")
	got, err := xdb.RedeemPairing(raw, sub)
	if err != nil {
		t.Fatal(err)
	}
	if got != sender {
		t.Errorf("paired identity = %q, want inbound sender %q", got, sender)
	}
	if !auth.Authorize(xdb, auth.Caller{Principal: sender}, "admin", "alice", nil) {
		t.Error("paired Discord DM sender remains anonymous")
	}
	if auth.Authorize(xdb, auth.Caller{Principal: chat}, "admin", "alice", nil) {
		t.Error("chat acquired the sender's authority")
	}
	observePairings(xdb, obdb, cfg)
	row := admission(t, obdb, chat)
	if row.Status != "approved" || row.UserSub != sub {
		t.Fatalf("admission = %+v", row)
	}
	if jid := unroutedJID(xdb, sub); jid != chat {
		t.Errorf("browser routing target = %q, want chat %q", jid, chat)
	}
	if !userOwnsMatch(xdb, sub, "room=dm/456") {
		t.Error("paired sender cannot route the DM chat")
	}
	if userOwnsMatch(xdb, sub, "room=user/123") {
		t.Error("sender identity offered as a chat route")
	}
}
