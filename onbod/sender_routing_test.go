package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/store"
)

func TestPairedDiscordDMDoesNotOwnSharedChannel(t *testing.T) {
	xdb, obdb := migratedDB(t)
	const chat = "discord:dm/456"
	const sender = "discord:user/123"
	const sub = "github:alice"
	seedInbound(t, xdb, chat, sender)
	mux := http.NewServeMux()
	(&admin{st: obdb}).mountOnboarding(mux)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("POST", "/v1/onboarding",
		strings.NewReader(`{"jid":"discord:dm/456","sender":"discord:user/123"}`)))
	if w.Code != http.StatusOK {
		t.Fatalf("onboarding: %d %s", w.Code, w.Body.String())
	}
	raw, err := xdb.IssuePairingLink(t.Context(), sender, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := xdb.RedeemPairing(raw, sub); err != nil {
		t.Fatal(err)
	}
	if err := xdb.CreateUserProfile(sub, "alice", "Alice"); err != nil {
		t.Fatal(err)
	}
	seedFolder(t, xdb, "alice")
	seedGrant(t, xdb, sub, "admin", "alice")
	seedInbound(t, xdb, "discord:channel/shared", sender)

	req := httptest.NewRequest("GET", "/onboard", nil)
	req.Header.Set("X-User-Sub", sub)
	for range 2 {
		w = httptest.NewRecorder()
		handleOnboard(w, req, xdb, obdb, config{})
		if w.Code != http.StatusOK {
			t.Fatalf("onboard: %d %s", w.Code, w.Body.String())
		}
		if strings.Contains(w.Body.String(), "discord:channel/shared") {
			t.Error("dashboard offers the shared channel as the user's chat")
		}
		if !strings.Contains(w.Body.String(), `<td>discord:dm/456</td><td>alice</td>`) {
			t.Error("dashboard omits the recorded Discord DM route")
		}
	}
	routes := routeRows(t, xdb)
	if len(routes) != 1 || routes[0].Match != "room=dm/456" {
		t.Errorf("auto-routes = %+v, want only the recorded Discord DM", routes)
	}
	w = postOnboardSplit(xdb, obdb, sub, url.Values{
		"action": {"add_route"}, "match": {"room=channel/shared"},
		"target": {"alice"}, "csrf": {"c"},
	})
	if w.Code != http.StatusForbidden {
		t.Errorf("shared-channel route control = %d, want 403: %s", w.Code, w.Body.String())
	}
	w = postOnboardSplit(xdb, obdb, sub, url.Values{
		"action": {"add_route"}, "match": {"room=dm/456"}, "target": {"alice"},
		"csrf": {"c"},
	})
	if w.Code != http.StatusSeeOther {
		t.Errorf("recorded DM route control = %d, want 303: %s", w.Code, w.Body.String())
	}
}

func TestUnresolvedOnboardingSenderWaitsForInbound(t *testing.T) {
	xdb, obdb := migratedDB(t)
	const chat = "discord:dm/456"
	if _, err := obdb.ImportOnboarding(t.Context(), []store.ArchiveOnboardingRow{
		{JID: chat, Status: "awaiting_message", Created: fixtureTS},
	}); err != nil {
		t.Fatal(err)
	}
	if err := xdb.PutMembership(chat, "github:alice", store.PairingAddedBy); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	cfg := config{gatedURL: srv.URL, authBaseURL: "https://example.test",
		svcToken: func(context.Context) (string, error) { return "service", nil }}
	promptUnprompted(xdb, obdb, cfg)
	observePairings(xdb, obdb, cfg)
	row := admission(t, obdb, chat)
	if row.Sender != "" || row.PromptedAt != "" || row.UserSub != "" || row.Status != "awaiting_message" {
		t.Fatalf("unresolved row advanced: %+v", row)
	}
	if err := xdb.PutMessage(core.Message{
		ID: "new", ChatJID: chat, Sender: "discord:user/123", Timestamp: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := obdb.InsertOnboarding(chat, "discord:user/123"); err != nil {
		t.Fatal(err)
	}
	observePairings(xdb, obdb, cfg)
	if row := admission(t, obdb, chat); row.UserSub != "" {
		t.Fatalf("chat edge admitted a different sender: %+v", row)
	}
}
