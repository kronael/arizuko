package main

import (
	"context"
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

func (c *onboardingReadCounter) Query(ctx context.Context, q objstore.Query) ([]objstore.Item, error) {
	if q.Entity == store.Onboarding {
		c.reads++
	}
	return c.Store.Query(ctx, q)
}

// One tick greets and checks pairings over a single read of the onboarding
// partition; three reads per 10 s tick was the cost (BUGS L829).
func TestTickReadsOnboardingOnce(t *testing.T) {
	db := testDB(t)
	seedAdmission(t, db, store.ArchiveOnboardingRow{
		JID: "telegram:user/1", Status: "awaiting_message", Created: "2026-08-06T00:00:00Z"})
	counter := &onboardingReadCounter{Store: db.Obj()}
	pollOnboarding(db, store.NewOver(counter), config{})
	if counter.reads != 1 {
		t.Fatalf("onboarding reads per tick = %d, want 1", counter.reads)
	}
}
