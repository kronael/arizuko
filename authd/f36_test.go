package main

// F36: a refresh-token family kill must not be outrun by the successor it was
// racing. `TestRefreshRotationRaceSingleWinner` (bugfix_test.go) covers the
// same invariant but only observes it when the scheduler happens to order the
// loser's revoke ahead of the winner's insert — it passes on an idle machine,
// so it can never be the proof that a fix landed. These tests FORCE that
// ordering.
//
// The barrier is a production seam, not a test hook: `Authd.grants` is a
// `GrantsFetcher` interface, and `Refresh` calls it exactly once per redeem. A
// fetcher that parks its FIRST caller pins one redeem mid-`Refresh` for as
// long as the test wants, and a competing redeem then runs start-to-finish
// inside that window. No sleeps, no `-race`-dependent luck, no injected pause
// in non-test code.

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kronael/arizuko/authd/authstore"
	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/objstore/dynstore"
)

// gateGrants parks the FIRST FetchGrants caller until release is closed, and
// closes entered when it gets there. Every later caller passes straight
// through — deliberately NOT sync.Once, whose second caller would block behind
// the first and deadlock the very interleave these tests construct.
type gateGrants struct {
	calls   atomic.Int64
	entered chan struct{}
	release chan struct{}
	snap    GrantsSnapshot
}

func newGateGrants(scope ...string) *gateGrants {
	return &gateGrants{
		entered: make(chan struct{}),
		release: make(chan struct{}),
		snap:    GrantsSnapshot{Scope: scope},
	}
}

func (g *gateGrants) FetchGrants(context.Context, string) (GrantsSnapshot, error) {
	if g.calls.Add(1) == 1 {
		close(g.entered)
		<-g.release
	}
	return g.snap, nil
}

// familyOf reads the lineage a raw refresh token belongs to.
func familyOf(t *testing.T, s objstore.Store, raw string) string {
	t.Helper()
	r, ok := newTestStore(s).RefreshTokenBySecret(raw)
	if !ok {
		t.Fatal("family of issued token: the token is not in the store")
	}
	return r.Family
}

// familyRows counts a family's rows: total, and how many are still live
// (neither revoked nor spent — i.e. usable to mint another access token).
//
// `total` is asserted by every caller BEFORE `live`, because `live == 0` is
// also what a query against the wrong family_id returns. Counting the rows
// that must exist is what stops these tests passing vacuously.
func familyRows(t *testing.T, db *dynstore.Store, fam string) (total, live int) {
	t.Helper()
	tokens := entityItems(t, db, authstore.RefreshTokens,
		objstore.Cmp{Attr: "family_id", Op: objstore.Eq, Value: fam})
	total = len(tokens)
	if familyRevoked(t, db, fam) {
		return total, 0
	}
	for _, it := range tokens {
		if _, spent := it.Attrs["used_at"]; !spent {
			live++
		}
	}
	return total, live
}

// A reuse-triggered family kill that lands while a concurrent redeem is
// mid-rotation must leave NO live row behind.
//
// The forced ordering, pre-fix, is the exact sequence BUGS F36 describes:
//
//	redeem A: lookup -> ClaimRefreshToken WINS -> [parked in FetchGrants]
//	redeem B: lookup sees used_at set -> RevokeFamily -> errReuse
//	redeem A: unparked -> rotateRefresh INSERTs the successor, revoked_at NULL
//
// The family was killed and a live 30-day credential walked out of it.
//
// Post-fix the roles swap — A parks BEFORE its claim, so B claims and rotates,
// then A loses the compare-and-set and revokes a family that already contains
// B's successor. Same barrier, same interleave, opposite outcome: the
// assertion is on the invariant (a killed family holds no live row), not on
// which caller won, so it is meaningful in both worlds.
func TestRefreshFamilyKillOutrunsNoSuccessor(t *testing.T) {
	db := testDB(t)
	a, err := newAuthd(db, 15*time.Minute, time.Hour, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	g := newGateGrants("tasks:read")
	a.grants = g

	r0, err := a.IssueRefresh("1", []string{"tasks:read"}, "")
	if err != nil {
		t.Fatal(err)
	}
	fam := familyOf(t, db, r0)

	type outcome struct {
		refresh string
		err     error
	}
	first := make(chan outcome, 1)
	go func() {
		_, nr, err := a.Refresh(context.Background(), r0)
		first <- outcome{nr, err}
	}()

	<-g.entered // redeem A is now parked inside Refresh, mid-rotation.

	// Redeem B runs start to finish inside that window: a second presentation
	// of the same one-time token, which is reuse by definition.
	_, secondRefresh, secondErr := a.Refresh(context.Background(), r0)
	close(g.release)
	firstOut := <-first

	// Exactly one of the two may hand out a successor; the other is the reuse
	// signal. (Which one is a post-/pre-fix detail, so accept either.)
	if firstOut.err == nil && secondErr == nil {
		t.Fatal("both concurrent redeems of a one-time token succeeded; the CAS must pick one")
	}
	if firstOut.err != nil && secondErr != nil {
		t.Fatalf("neither redeem won: first=%v second=%v", firstOut.err, secondErr)
	}

	// Anti-vacuity: the original plus exactly one successor must be on disk.
	// A wrong family_id, or a rollback that lost the successor entirely, shows
	// up here rather than sailing through the live==0 check below.
	total, live := familyRows(t, db, fam)
	if total != 2 {
		t.Fatalf("family %s: want 2 rows (original + lone successor), got %d", fam, total)
	}
	if live != 0 {
		t.Fatalf("family %s was killed for reuse but %d row(s) are still live: "+
			"the successor outran the revoke (BUGS F36)", fam, live)
	}

	// And end to end: whichever successor was handed out must be dead.
	successor := firstOut.refresh
	if successor == "" {
		successor = secondRefresh
	}
	if successor == "" {
		t.Fatal("no successor was returned by either redeem")
	}
	if _, _, err := a.Refresh(context.Background(), successor); err == nil {
		t.Fatal("successor of a reuse-killed family still refreshes")
	}
}

// The second kill source is logout (authd/oauth.go — `RevokeFamily` on the
// caller's own cookie). It races the same insert, and the transaction alone
// does not stop it: a revoke that commits BEFORE the rotation's transaction
// opens is not an interleave at all, so the claim has to observe the family's
// revoked state to refuse. That is the `revoked_at IS NULL` half of the
// compare-and-set.
//
// Forced ordering:
//
//	redeem:  lookup (family live) -> [parked in FetchGrants]
//	logout:  RevokeFamily -> every row revoked
//	redeem:  unparked -> claim must FAIL and mint nothing
func TestRefreshAfterLogoutRevokeMintsNoSuccessor(t *testing.T) {
	db := testDB(t)
	a, err := newAuthd(db, 15*time.Minute, time.Hour, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	g := newGateGrants("tasks:read")
	a.grants = g

	r0, err := a.IssueRefresh("1", []string{"tasks:read"}, "")
	if err != nil {
		t.Fatal(err)
	}
	fam := familyOf(t, db, r0)

	done := make(chan error, 1)
	go func() {
		_, _, err := a.Refresh(context.Background(), r0)
		done <- err
	}()

	<-g.entered
	// This is verbatim what oauth.logout does with the caller's cookie.
	if _, err := newTestStore(db).RevokeFamily(fam); err != nil {
		t.Fatal(err)
	}
	close(g.release)

	if err := <-done; err == nil {
		t.Fatal("a refresh that resumed after its family was revoked must not succeed")
	}

	// Anti-vacuity: the original row must still be there to have been revoked.
	total, live := familyRows(t, db, fam)
	if total != 1 {
		t.Fatalf("family %s: want 1 row (the original; logout precedes any successor), got %d", fam, total)
	}
	if live != 0 {
		t.Fatalf("family %s was revoked at logout but %d row(s) are still live", fam, live)
	}
}
