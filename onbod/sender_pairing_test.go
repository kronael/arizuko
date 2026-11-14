package main

import (
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

	cfg := config{authBaseURL: "https://example.test"}
	promptUnprompted(xdb, obdb, cfg, awaitingRows(t, obdb))
	deliveries, err := xdb.PendingOutbound(t.Context(), time.Now().Add(time.Second), 10)
	if err != nil || len(deliveries) != 1 || deliveries[0].ChatJID != chat {
		t.Fatalf("queued greeting: %v %v", deliveries, err)
	}
	raw := strings.TrimPrefix(deliveries[0].Content, "Set up your account: https://example.test/pair/")
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
	observePairings(xdb, obdb, cfg, awaitingRows(t, obdb))
	row := admission(t, obdb, chat)
	if row.Status != "approved" || row.UserSub != sub {
		t.Fatalf("admission = %+v", row)
	}
}
