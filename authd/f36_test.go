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
	"database/sql"
	"sync/atomic"
	"testing"
	"time"
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
func familyOf(t *testing.T, db *sql.DB, raw string) string {
	t.Helper()
	var fam string
	if err := db.QueryRow(
		`SELECT family_id FROM refresh_tokens WHERE token_hash = ?`, hashToken(raw)).
		Scan(&fam); err != nil {
		t.Fatalf("family of issued token: %v", err)
	}
	return fam
}

// familyRows counts a family's rows: total, and how many are still live
// (neither revoked nor spent — i.e. usable to mint another access token).
//
// `total` is asserted by every caller BEFORE `live`, because `live == 0` is
// also what a query against the wrong family_id returns. Counting the rows
// that must exist is what stops these tests passing vacuously.
func familyRows(t *testing.T, db *sql.DB, fam string) (total, live int) {
	t.Helper()
	if err := db.QueryRow(
		`SELECT COUNT(*), COUNT(CASE WHEN revoked_at IS NULL AND used_at IS NULL THEN 1 END)
		   FROM refresh_tokens WHERE family_id = ?`, fam).Scan(&total, &live); err != nil {
		t.Fatalf("count family rows: %v", err)
	}
	return total, live
}

// A reuse-triggered family kill that lands while a concurrent redeem is
// mid-rotation must leave NO live row behind.
//
// The forced ordering, pre-fix, is the exact sequence BUGS F36 describes:
//
//	redeem A: lookup -> markRefreshUsed WINS -> [parked in FetchGrants]
//	redeem B: lookup sees used_at set -> revokeFamily -> errReuse
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
	db := concurrentDB(t)
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

// The transaction in claimAndRotateRefresh, pinned without any timing.
//
// The barrier tests above are the end-to-end F36 regression, but neither of
// them falsifies the TRANSACTION on its own: moving the grants re-snapshot
// ahead of the claim (which the transaction requires anyway — a remote call
// cannot sit inside a SQLite write lock) already shrinks the win→insert gap
// from an HTTP round trip to two adjacent local statements, and a barrier
// cannot be steered into a gap that small. Verified, not assumed: with the
// transaction stripped and everything else kept, both barrier tests still
// pass. A narrower race is still a race, so the transaction needs its own pin.
//
// A BEFORE INSERT trigger makes the successor's INSERT fail on demand. Inside
// one transaction the failure rolls the claim back too, so the presented token
// is untouched and the client can retry. Two autocommit statements leave
// `used_at` set: the token is burnt, no successor exists, and the user is
// logged out by an error that changed nothing else.
func TestRefreshClaimRollsBackWhenSuccessorInsertFails(t *testing.T) {
	db := concurrentDB(t)
	raw, err := issueRefresh(db, "1", []string{"tasks:read"}, "", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	fam := familyOf(t, db, raw)
	if _, err := db.Exec(
		`CREATE TRIGGER f36_block_successor BEFORE INSERT ON refresh_tokens
		   BEGIN SELECT RAISE(ABORT, 'successor insert blocked'); END`); err != nil {
		t.Fatal(err)
	}

	newRaw, won, err := claimAndRotateRefresh(db, raw, fam, "1", []string{"tasks:read"}, "", time.Hour)
	if err == nil {
		t.Fatal("a blocked successor INSERT must surface as an error, not a silent no-op")
	}
	if won || newRaw != "" {
		t.Fatalf("failed rotation reported won=%v newRaw=%q; it rotated nothing", won, newRaw)
	}

	// Anti-vacuity: the original row must still be the only row, and it must
	// still be there at all — a query that found nothing would satisfy the
	// used_at check below for the wrong reason.
	total, live := familyRows(t, db, fam)
	if total != 1 {
		t.Fatalf("family %s: want the 1 original row (the successor was blocked), got %d", fam, total)
	}
	if live != 1 {
		t.Fatalf("family %s: the presented token was consumed by a rotation that failed — "+
			"claim and successor INSERT are not in one transaction (BUGS F36)", fam)
	}
}

// The second kill source is logout (authd/oauth.go:413 — `revokeFamily` on the
// caller's own cookie). It races the same insert, and the transaction alone
// does not stop it: a revoke that commits BEFORE the rotation's transaction
// opens is not an interleave at all, so the claim has to observe the family's
// revoked state to refuse. That is the `revoked_at IS NULL` half of the
// compare-and-set.
//
// Forced ordering:
//
//	redeem:  lookup (family live) -> [parked in FetchGrants]
//	logout:  revokeFamily -> every row revoked
//	redeem:  unparked -> claim must FAIL and mint nothing
func TestRefreshAfterLogoutRevokeMintsNoSuccessor(t *testing.T) {
	db := concurrentDB(t)
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
	if err := revokeFamily(db, fam); err != nil {
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
