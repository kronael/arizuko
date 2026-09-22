package store

// turn_results_test.go proves the one property this entity has: the claim. A
// second report of one turn's outcome must answer false and write nothing, or
// the agent's prose answer is delivered to the chat twice.
//
// THE INTERLEAVE IS WRITTEN OUT rather than forced, and that is the property
// rather than an omission. conformance.SlipAt fires between a read and the write
// that follows it, and RecordTurnResult performs no read at all: the decision IS
// the create's own condition, so two writers are serialised by the engine. One
// call commits, the next is the claimant that lost, and the order is
// deterministic because each is its own transaction.

import (
	"context"
	"testing"
)

func TestTheSecondReportOfOneTurnClaimsNothing(t *testing.T) {
	s := openEntities(t)
	first, err := s.RecordTurnResult("g1", "msg-1", "sess-a", "success", "")
	if err != nil {
		t.Fatal(err)
	}
	if !first {
		t.Error("the first report did not claim the turn, so nothing delivers its answer")
	}
	second, err := s.RecordTurnResult("g1", "msg-1", "sess-b", "error", "")
	if err != nil {
		t.Fatal(err)
	}
	if second {
		t.Error("a second report of one turn claimed it too; the agent's answer is " +
			"delivered to the chat twice and round_done is published twice")
	}
	// A REFUSED CLAIM WRITES NOTHING. There is no merge on the refusal here,
	// unlike every other create-if-absent in this package: the outcome a caller
	// acted on is the one that stays.
	info, err := s.GetTurnResult("g1", "msg-1")
	if err != nil {
		t.Fatal(err)
	}
	if info.SessionID != "sess-a" || info.Status != "success" {
		t.Errorf("the turn reads back %+v, want the FIRST report (sess-a/success): the "+
			"refused report merged into the item the caller already acted on", info)
	}
}

func TestOneTurnIdInTwoFoldersIsTwoTurns(t *testing.T) {
	s := openEntities(t)
	a, err := s.RecordTurnResult("g1", "msg-1", "", "success", "")
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.RecordTurnResult("g2", "msg-1", "", "success", "")
	if err != nil {
		t.Fatal(err)
	}
	if !a || !b {
		t.Errorf("one turn id in two folders claimed %v/%v, want both: the folder is half "+
			"the coordinate", a, b)
	}
}

func TestDistinctTurnsInOneFolderEachClaimOnce(t *testing.T) {
	s := openEntities(t)
	for _, id := range []string{"m1", "m2", "m3"} {
		ok, err := s.RecordTurnResult("g1", id, "s", "success", "")
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			t.Errorf("turn %s did not claim; the guard refuses a turn that is not a repeat", id)
		}
	}
}

// A turn with no outcome is "pending". The browser's stream closes on a
// finished round, so a turn nobody has reported must read as still running
// (BUGS L83).
func TestAnUnreportedTurnIsPending(t *testing.T) {
	s := openEntities(t)
	info, err := s.GetTurnResult("g1", "never-ran")
	if err != nil {
		t.Fatalf("an unreported turn must not error: %v", err)
	}
	if info.Status != turnResultPending {
		t.Fatalf("status = %q, want %q", info.Status, turnResultPending)
	}
}

// TurnResultRecorded is the same answer as a bool, and both of its consumers act
// on a false: the run-response path overwrites the agent's own session id, and
// the silent-turn alarm fires at an operator.
func TestTurnResultRecordedSeparatesRecordedFromAbsent(t *testing.T) {
	s := openEntities(t)
	if _, err := s.RecordTurnResult("g1", "msg-1", "sess-a", "success", ""); err != nil {
		t.Fatal(err)
	}
	recorded, err := s.TurnResultRecorded("g1", "msg-1")
	if err != nil || !recorded {
		t.Fatalf("recorded=%v err=%v, want true", recorded, err)
	}
	recorded, err = s.TurnResultRecorded("g1", "other")
	if err != nil || recorded {
		t.Fatalf("recorded=%v err=%v on a turn nobody reported, want false and no error",
			recorded, err)
	}
}

// The coordinate joins two fields on one byte, so a value carrying that byte
// would split the key somewhere the read never puts it back together. It is
// REFUSED rather than written.
func TestACoordinateFieldCarryingTheSeparatorIsRefused(t *testing.T) {
	s := openEntities(t)
	if _, err := s.RecordTurnResult("g1"+KeySep+"x", "msg-1", "", "ok", ""); err == nil {
		t.Error("a folder carrying the coordinate separator was accepted")
	}
	if _, err := s.RecordTurnResult("g1", "msg"+KeySep+"1", "", "ok", ""); err == nil {
		t.Error("a turn id carrying the coordinate separator was accepted")
	}
	if n, err := s.CountTurnResults(context.Background()); err != nil || n != 0 {
		t.Fatalf("%d items landed (err=%v); a refused coordinate wrote one anyway", n, err)
	}
}

// The agent reports why its turn failed through submit_turn. routd tells the
// user, and the container's stderr tail says the same thing worse — truncated
// mid-word and mixed with ant's own log lines, so a user was shown
// "ake says no" for an API Error: 400 (BUGS L349).
func TestTurnFailureKeepsTheAgentsOwnReport(t *testing.T) {
	s := openEntities(t)
	if _, err := s.RecordTurnResult("g1", "msg-1", "sess", "error",
		"API Error: 400 fake says no"); err != nil {
		t.Fatal(err)
	}
	if got := s.TurnFailure("g1", "msg-1"); got != "API Error: 400 fake says no" {
		t.Errorf("TurnFailure = %q; the user sees the stderr tail instead", got)
	}
	// A turn that reported none answers empty, so the caller falls back rather
	// than telling the user nothing at all.
	if _, err := s.RecordTurnResult("g1", "msg-2", "sess", "error", ""); err != nil {
		t.Fatal(err)
	}
	if got := s.TurnFailure("g1", "msg-2"); got != "" {
		t.Errorf("TurnFailure = %q, want empty", got)
	}
	if got := s.TurnFailure("g1", "never-recorded"); got != "" {
		t.Errorf("TurnFailure for an unrecorded turn = %q, want empty", got)
	}
}
