package store

// invite_redemptions_test.go proves the two properties the create-world path
// rests on: the redemption a caller reads is their MOST RECENT one, and spending
// it is a claim that exactly one caller wins.

import (
	"context"
	"testing"
)

// seedRedemption records one redemption through the writer the invite path uses.
func seedRedemption(t *testing.T, s *Store, userSub, glob string) {
	t.Helper()
	at, err := redemptionNowStamp()
	if err != nil {
		t.Fatal(err)
	}
	w, err := redemptionWrite(userSub, at, glob)
	if err != nil {
		t.Fatal(err)
	}
	recorded, err := s.TryWriteEntities(context.Background(), nil, w)
	if err != nil || !recorded {
		t.Fatalf("seed a redemption for %s: recorded=%v err=%v", userSub, recorded, err)
	}
}

// A caller holding two redemptions is answering the invite they just followed,
// so the read answers the most recent — and the position is what orders them,
// which is why the instant is the identity.
func TestARedemptionReadAnswersTheMostRecentOne(t *testing.T) {
	s := openEntities(t)
	ctx := context.Background()
	seedRedemption(t, s, "github:bob", "first/")
	seedRedemption(t, s, "github:bob", "second/")
	seedRedemption(t, s, "github:alice", "other/")

	r, held, err := s.LatestRedemption(ctx, "github:bob")
	if err != nil || !held {
		t.Fatalf("held=%v err=%v", held, err)
	}
	if r.TargetGlob != "second/" {
		t.Fatalf("the read answered %q, want the most recent: a world is created under the "+
			"parent this names", r.TargetGlob)
	}
	// The person is the PARTITION, so nobody else's redemption is reachable.
	other, held, err := s.LatestRedemption(ctx, "github:alice")
	if err != nil || !held || other.TargetGlob != "other/" {
		t.Fatalf("alice read %+v held=%v err=%v", other, held, err)
	}
	if _, held, err := s.LatestRedemption(ctx, "github:nobody"); err != nil || held {
		t.Fatalf("a person with no redemption answered held=%v err=%v", held, err)
	}
}

// Spending is a CLAIM: two create-world posts that derived the same redemption
// cannot both spend it, so one invite redemption is one world. The delete's own
// refusal is the answer, and it is what a resubmitted form meets.
func TestARedemptionIsSpentExactlyOnce(t *testing.T) {
	s := openEntities(t)
	ctx := context.Background()
	seedRedemption(t, s, "github:bob", "acme/")
	r, held, err := s.LatestRedemption(ctx, "github:bob")
	if err != nil || !held {
		t.Fatalf("held=%v err=%v", held, err)
	}

	spent, err := s.SpendRedemption(ctx, "github:bob", r)
	if err != nil || !spent {
		t.Fatalf("the first spend answered %v err=%v", spent, err)
	}
	// The SAME redemption, spent again — a resubmitted form, a second tab.
	spent, err = s.SpendRedemption(ctx, "github:bob", r)
	if err != nil {
		t.Fatal(err)
	}
	if spent {
		t.Fatal("one redemption was spent twice; a replay derives the same parent and " +
			"creates a second world under it")
	}
	if _, held, err := s.LatestRedemption(ctx, "github:bob"); err != nil || held {
		t.Fatalf("a spent redemption is still readable: held=%v err=%v", held, err)
	}
}

// Two redemptions are two worlds: spending one leaves the other, so a caller who
// followed two invites still gets both.
func TestSpendingOneRedemptionLeavesTheOther(t *testing.T) {
	s := openEntities(t)
	ctx := context.Background()
	seedRedemption(t, s, "github:bob", "first/")
	seedRedemption(t, s, "github:bob", "second/")

	r, _, err := s.LatestRedemption(ctx, "github:bob")
	if err != nil {
		t.Fatal(err)
	}
	if spent, err := s.SpendRedemption(ctx, "github:bob", r); err != nil || !spent {
		t.Fatalf("spend=%v err=%v", spent, err)
	}
	next, held, err := s.LatestRedemption(ctx, "github:bob")
	if err != nil || !held {
		t.Fatalf("the second redemption went with the first: held=%v err=%v", held, err)
	}
	if next.TargetGlob != "first/" {
		t.Fatalf("the remaining redemption is %q, want first/", next.TargetGlob)
	}
}

// A caller cannot spend a redemption it never read: the position is unexported,
// so the only Redemption a caller holds is one the read handed it, and the zero
// value names no item.
func TestAZeroRedemptionSpendsNothing(t *testing.T) {
	s := openEntities(t)
	ctx := context.Background()
	seedRedemption(t, s, "github:bob", "acme/")
	if _, err := s.SpendRedemption(ctx, "github:bob", Redemption{}); err == nil {
		t.Fatal("a redemption nobody read was accepted for spending")
	}
	if n, err := s.CountRedemptions(ctx, "github:bob"); err != nil || n != 1 {
		t.Fatalf("%d redemptions left (err=%v), want the one that was never spent", n, err)
	}
}

// The move that carries a legacy row builds the position out of the instant it
// was redeemed at and the row number that broke a tie, so two redemptions of one
// second keep the order the read answered them in.
func TestTheCarriedRedemptionsKeepTheirOrder(t *testing.T) {
	s := openEntities(t)
	ctx := context.Background()
	if _, err := s.ImportOnbod(ctx, OnbodSnapshot{Redemptions: []RedemptionRow{
		{RowID: 1, UserSub: "github:bob", TargetGlob: "first/",
			RedeemedAt: "2026-08-07T00:00:00Z"},
		{RowID: 2, UserSub: "github:bob", TargetGlob: "second/",
			RedeemedAt: "2026-08-07T00:00:00Z"},
	}}); err != nil {
		t.Fatalf("carry the redemptions: %v", err)
	}
	r, held, err := s.LatestRedemption(ctx, "github:bob")
	if err != nil || !held {
		t.Fatalf("held=%v err=%v", held, err)
	}
	if r.TargetGlob != "second/" {
		t.Fatalf("the most recent carried redemption is %q, want second/: two rows of one "+
			"second lost the order their ids gave them", r.TargetGlob)
	}
}
