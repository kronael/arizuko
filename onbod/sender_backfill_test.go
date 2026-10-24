package main

import (
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/store"
)

func TestBackfillOnboardingSenders(t *testing.T) {
	xdb, obdb := migratedDB(t)
	rows := []store.ArchiveOnboardingRow{
		{JID: "discord:dm/1", Status: "awaiting_message", Created: fixtureTS, PromptedAt: fixtureTS},
		{JID: "discord:dm/2", Status: "approved", UserSub: "github:alice", Gate: "*",
			Created: fixtureTS, PromptedAt: fixtureTS, QueuedAt: fixtureTS, AdmittedAt: fixtureTS},
		{JID: "discord:dm/3", Status: "awaiting_message", Created: fixtureTS},
		{JID: "discord:dm/4", Status: "awaiting_message", Created: fixtureTS},
		{JID: "discord:dm/5", Sender: "discord:user/original", Status: "awaiting_message",
			Created: fixtureTS, PromptedAt: fixtureTS},
	}
	if _, err := obdb.ImportOnboarding(t.Context(), rows); err != nil {
		t.Fatal(err)
	}
	for _, jid := range []string{"discord:dm/1", "discord:dm/2", "discord:dm/5"} {
		if err := xdb.PutMessage(core.Message{
			ID: "inbound", ChatJID: jid, Sender: "discord:user/123", Timestamp: time.Now(),
		}); err != nil {
			t.Fatal(err)
		}
		if err := xdb.PutMessage(core.Message{
			ID: "outbound", ChatJID: jid, Sender: "bot", BotMsg: true, Timestamp: time.Now(),
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := xdb.PutMessage(core.Message{
		ID: "empty", ChatJID: "discord:dm/4", Timestamp: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := backfillOnboardingSenders(xdb, obdb); err != nil {
		t.Fatal(err)
	}
	rows[0].Sender = "discord:user/123"
	rows[0].PromptedAt = ""
	rows[1].Sender = "discord:user/123"
	for _, want := range rows {
		got := admission(t, obdb, want.JID)
		if !reflect.DeepEqual(got, store.OnboardingRow(want)) {
			t.Errorf("backfilled row = %+v, want %+v", got, want)
		}
	}
	if err := backfillOnboardingSenders(xdb, obdb); err != nil {
		t.Fatal(err)
	}
}

func TestBackfillOnboardingSendersReturnsStoreErrors(t *testing.T) {
	xdb, obdb := migratedDB(t)
	if err := backfillOnboardingSenders(xdb, xdb); err == nil {
		t.Fatal("missing onboarding entity did not fail startup repair")
	}
	if _, err := obdb.ImportOnboarding(t.Context(), []store.ArchiveOnboardingRow{
		{JID: "discord:dm/456", Status: "awaiting_message", Created: fixtureTS},
	}); err != nil {
		t.Fatal(err)
	}
	if err := backfillOnboardingSenders(ownedDB(t), obdb); err == nil {
		t.Fatal("missing message entity did not fail startup repair")
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
	promptUnprompted(xdb, obdb, config{})
	observePairings(xdb, obdb, config{})
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
	observePairings(xdb, obdb, config{})
	if row := admission(t, obdb, chat); row.UserSub != "" {
		t.Fatalf("chat edge admitted a different sender: %+v", row)
	}
}

func TestPairedSenderBrowserChats(t *testing.T) {
	xdb, _ := migratedDB(t)
	const sender = "discord:user/123"
	const sub = "github:alice"
	seedMembership(t, xdb, sender, sub, store.PairingAddedBy)
	seedMembership(t, xdb, "discord:user/other", sub, store.PairingAddedBy)
	for i, row := range []core.Message{
		{ID: "first", ChatJID: "discord:dm/1", Sender: sender},
		{ID: "second", ChatJID: "discord:dm/2", Sender: sender},
		{ID: "shared", ChatJID: "discord:dm/2", Sender: "discord:user/other"},
		{ID: "foreign", ChatJID: "discord:dm/3", Sender: "discord:user/stranger"},
	} {
		row.Timestamp = time.Now().Add(time.Duration(i) * time.Second)
		if err := xdb.PutMessage(row); err != nil {
			t.Fatal(err)
		}
	}
	if got, want := membershipJIDs(xdb, sub), []string{"discord:dm/1", "discord:dm/2"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("browser chats = %v, want %v", got, want)
	}
	if userOwnsMatch(xdb, sub, "room=dm/3") || userOwnsMatch(xdb, sub, "room=user/123") {
		t.Fatal("browser owns an unattested chat")
	}
	seedRoute(t, xdb, 0, "room=dm/2", "alice")
	if got := unroutedJID(xdb, sub); got != "discord:dm/1" {
		t.Fatalf("unrouted chat = %q", got)
	}
	w := httptest.NewRecorder()
	renderDashboard(w, xdb, sub, "alice")
	if !strings.Contains(w.Body.String(), `<td>discord:dm/2</td><td>alice</td>`) {
		t.Fatal("dashboard omits paired sender's chat route")
	}
}
