package main

import (
	"context"
	"fmt"
	"testing"

	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/store"
)

func awaitingRows(t *testing.T, db *store.Store) []store.OnboardingRow {
	t.Helper()
	rows, err := db.AwaitingOnboarding()
	if err != nil {
		t.Fatalf("read awaiting admissions: %v", err)
	}
	return rows
}

type onboardingReadCounter struct {
	objstore.Store
	reads int
}

type unreadableGates struct{ objstore.Store }

func (s unreadableGates) Query(ctx context.Context, q objstore.Query) ([]objstore.Item, error) {
	if q.Entity == store.OnboardingGates {
		return nil, fmt.Errorf("gate store unavailable")
	}
	return s.Store.Query(ctx, q)
}

func TestAdmissionKeepsWorkOnUnreadableGates(t *testing.T) {
	s := testDB(t)
	if _, err := s.InsertOnboarding("chat", "sender"); err != nil {
		t.Fatal(err)
	}
	broken := store.NewOver(unreadableGates{s.Obj()})
	if err := admitJID(broken, config{}, "chat", "user"); err == nil {
		t.Fatal("unreadable gates admitted caller")
	}
	if rows := awaitingRows(t, s); len(rows) != 1 {
		t.Fatalf("pending work consumed: %v", rows)
	}
}

func (c *onboardingReadCounter) Query(ctx context.Context, q objstore.Query) ([]objstore.Item, error) {
	if q.Entity == store.Onboarding {
		c.reads++
	}
	return c.Store.Query(ctx, q)
}

func TestIdleTickReadsNoOnboardingRows(t *testing.T) {
	db := testDB(t)
	for i := range 40 {
		seedAdmission(t, db, store.ArchiveOnboardingRow{JID: fmt.Sprint(i), Status: "awaiting_message", PromptedAt: "2026-08-06T00:00:00Z"})
	}
	counter := &onboardingReadCounter{Store: db.Obj()}
	pollOnboarding(db, store.NewOver(counter), config{})
	if counter.reads != 0 {
		t.Fatalf("idle tick read onboarding %d times", counter.reads)
	}
}

func TestPairingBeforeOnboardingAndReprompt(t *testing.T) {
	xdb, obdb := migratedDB(t)
	if err := xdb.AddMembership("sender", "github:user", store.PairingAddedBy); err != nil {
		t.Fatal(err)
	}
	consumePairings(xdb, obdb, config{})
	if _, err := obdb.InsertOnboarding("chat", "sender"); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		pollOnboarding(xdb, obdb, config{})
		row := admission(t, obdb, "chat")
		if row.Status != "approved" || row.UserSub != "github:user" {
			t.Fatalf("known pairing lost: %+v", row)
		}
		if err := obdb.RepromptOnboarding("chat"); err != nil {
			t.Fatal(err)
		}
	}
}
