package store

import (
	"maps"
	"testing"
	"time"
)

func TestOnboardingSenderPersistsAndArchives(t *testing.T) {
	s := openEntities(t)
	const chat = "discord:dm/456"
	const sender = "discord:user/123"
	if err := s.InsertOnboarding(chat, ""); err == nil {
		t.Fatal("empty sender accepted")
	}
	if err := s.InsertOnboarding(chat, sender); err != nil {
		t.Fatal(err)
	}
	if err := s.AdmitOnboarding(chat, "github:alice"); err != nil {
		t.Fatal(err)
	}
	before := maps.Clone(admissionItem(t, s, chat).Attrs)
	if err := s.InsertOnboarding(chat, "discord:user/other"); err != nil {
		t.Fatal(err)
	}
	if after := admissionItem(t, s, chat).Attrs; !maps.Equal(before, after) {
		t.Fatalf("repeat miss changed sender or verdict: %v -> %v", before, after)
	}
	rows, err := s.ListOnboarding("")
	if err != nil || len(rows) != 1 || rows[0].Sender != sender {
		t.Fatalf("onboarding rows = %v, %v", rows, err)
	}
	archive, err := s.ExportOnboarding(t.Context())
	if err != nil || len(archive) != 1 || archive[0].Sender != sender {
		t.Fatalf("onboarding archive = %v, %v", archive, err)
	}
	restored := openEntities(t)
	if _, err := restored.ImportOnboarding(t.Context(), archive); err != nil {
		t.Fatal(err)
	}
	if got := admissionItem(t, restored, chat).Attrs; !maps.Equal(before, got) {
		t.Fatalf("restored onboarding = %v, want %v", got, before)
	}
}

func TestInsertOnboardingSenderPreservesVerdicts(t *testing.T) {
	for _, status := range []string{statusAwaitingMessage, statusQueued, statusApproved, statusRefused} {
		t.Run(status, func(t *testing.T) {
			s := openEntities(t)
			row := ArchiveOnboardingRow{
				JID: "discord:dm/456", Status: status, UserSub: "github:alice", Gate: "*",
				Created: onbodTS(time.Now()), PromptedAt: onbodTS(time.Now()),
				QueuedAt: onbodTS(time.Now()), AdmittedAt: onbodTS(time.Now()),
			}
			if _, err := s.ImportOnboarding(t.Context(), []ArchiveOnboardingRow{row}); err != nil {
				t.Fatal(err)
			}
			want := maps.Clone(admissionItem(t, s, row.JID).Attrs)
			want[attrOnbSender] = "discord:user/123"
			if status == statusAwaitingMessage {
				delete(want, attrOnbPromptedAt)
			}
			if err := s.InsertOnboarding(row.JID, "discord:user/123"); err != nil {
				t.Fatal(err)
			}
			if err := s.InsertOnboarding(row.JID, "discord:user/other"); err != nil {
				t.Fatal(err)
			}
			if got := admissionItem(t, s, row.JID).Attrs; !maps.Equal(got, want) {
				t.Fatalf("backfilled onboarding = %v, want %v", got, want)
			}
		})
	}
}

func TestInsertOnboardingResolvesSenderAndRearmsGreeting(t *testing.T) {
	s := openEntities(t)
	row := ArchiveOnboardingRow{
		JID: "discord:dm/456", Status: statusAwaitingMessage,
		Created: onbodTS(time.Now()), PromptedAt: onbodTS(time.Now()),
	}
	if _, err := s.ImportOnboarding(t.Context(), []ArchiveOnboardingRow{row}); err != nil {
		t.Fatal(err)
	}
	if err := s.InsertOnboarding(row.JID, "discord:user/123"); err != nil {
		t.Fatal(err)
	}
	got := admissionItem(t, s, row.JID).Attrs
	if _, prompted := got[attrOnbPromptedAt]; prompted || got[attrOnbSender] != "discord:user/123" {
		t.Fatalf("resolved admission = %v", got)
	}
}
