package routd

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore/dynstore/dyntest"
)

func memDB(t *testing.T) *DB {
	t.Helper()
	d, err := OpenMem()
	if err != nil {
		t.Fatalf("OpenMem: %v", err)
	}
	// The index belongs to the process that answers searches, which in a test
	// is this one — routd's own main does the same after its open.
	d.StartSearch()
	t.Cleanup(func() { d.Close() })
	return d
}

func TestPendingAction_RoundTrip(t *testing.T) {
	d := memDB(t)
	p := PendingAction{ID: "a1", GroupFolder: "atlas", Tool: "mcp:delete"}
	if err := d.PutPendingAction(p); err != nil {
		t.Fatalf("PutPendingAction: %v", err)
	}
	got, ok := pendingActionRow(d, "a1")
	if !ok {
		t.Fatal("row not found")
	}
	if got.Status != PendingHeld || got.Tool != "mcp:delete" || got.CreatedAt == "" {
		t.Fatalf("unexpected row: %+v", got)
	}
}

func TestPendingAction_RejectsIncomplete(t *testing.T) {
	d := memDB(t)
	if err := d.PutPendingAction(PendingAction{GroupFolder: "atlas", Tool: "t"}); err == nil {
		t.Fatal("a row with no id must be refused, not written")
	}
}

func TestResolvePendingAction_OnlyHeldMoves(t *testing.T) {
	d := memDB(t)
	if err := d.PutPendingAction(PendingAction{
		ID: "a1", GroupFolder: "atlas", Tool: "t",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := resolvePending(d, "a1", PendingApproved, "op", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := resolvePending(d, "a1", PendingRejected, "op2", ""); err == nil {
		t.Fatal("a resolved row must not be re-resolved")
	}
}

func TestResolvePendingAction_RejectsUnknownVerdict(t *testing.T) {
	d := memDB(t)
	if _, err := resolvePending(d, "a1", "maybe", "op", ""); err == nil {
		t.Fatal("only approved/rejected are verdicts")
	}
}

func TestPendingAction_ExpiryIsLazy(t *testing.T) {
	d := memDB(t)
	past := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
	if err := d.PutPendingAction(PendingAction{
		ID: "a1", GroupFolder: "atlas", Tool: "t", ExpiresAt: past,
	}); err != nil {
		t.Fatal(err)
	}
	got, _ := pendingActionRow(d, "a1")
	if got.Status != PendingExpired {
		t.Fatalf("status = %q, want expired without any GC job", got.Status)
	}
}

func TestListPendingActions_FolderAndStatus(t *testing.T) {
	d := memDB(t)
	for _, p := range []PendingAction{
		{ID: "a1", GroupFolder: "atlas", Tool: "t1"},
		{ID: "b1", GroupFolder: "other", Tool: "t2"},
	} {
		if err := d.PutPendingAction(p); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := d.ListPendingActions("atlas", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ID != "a1" {
		t.Fatalf("folder filter leaked: %+v", rows)
	}
	all, err := d.ListPendingActions("", PendingHeld)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("want 2 held rows across folders, got %d", len(all))
	}
}

func TestStringArgs_SkipsNonScalars(t *testing.T) {
	got := stringArgs(map[string]any{
		"host": "x.com", "on": true, "n": float64(3), "obj": map[string]any{"a": 1},
	})
	if got["host"] != "x.com" || got["on"] != "true" || got["n"] != "3" {
		t.Fatalf("scalar flattening wrong: %+v", got)
	}
	if _, ok := got["obj"]; ok {
		t.Fatal("a non-scalar arg has no glob meaning and must not be matchable")
	}
}

// TestMain makes a hold give up AT ONCE by default: HoldWait 0 ends the wait on
// the first pass, expires the row and hands the agent the timeout refusal. A test
// about the wait itself sets HoldWait through waitForVerdict and answers the call
// from another goroutine. Without this each hold test would sit through the real
// HITL_WAIT.
func TestMain(m *testing.M) {
	if err := dyntest.Setup(); err != nil {
		fmt.Fprintf(os.Stderr, "dyntest: %v\n", err)
		os.Exit(1)
	}
	HoldWait, hitlPoll = 0, time.Millisecond
	code := m.Run()
	dyntest.Shutdown()
	os.Exit(code)
}

// TestHoldGate_HoldApproveRun is spec 5/19's acceptance path end to end at the
// gate: a hold rule suspends the call and records a row, an approval runs THAT
// call inside the same turn, and a tool no rule covers is never delayed.
func TestHoldGate_HoldApproveRun(t *testing.T) {
	waitForVerdict(t)

	d := memDB(t)
	srv := &Server{db: d}
	holdRule(t, d, "mcp:delete", auth.EffectHoldBlock)
	gate := srv.holdGate(turnMCP{folder: "atlas"})
	if gate == nil {
		t.Fatal("a normal folder turn must get a gate")
	}
	args := map[string]any{"target": "prod"}

	out := make(chan bool, 1)
	go func() {
		_, held := gate("mcp:delete", args)
		out <- held
	}()

	id := awaitHeldID(t, d)
	row, ok := pendingActionRow(d, id)
	if !ok || row.Tool != "mcp:delete" || row.GroupFolder != "atlas" {
		t.Fatalf("held row not recorded: %+v", row)
	}
	// A tool no rule covers runs even while another call of the same turn waits.
	if _, held := gate("mcp:send", args); held {
		t.Fatal("an unheld tool must run inline")
	}

	if _, err := resolvePending(d, id, PendingApproved, "google:op", ""); err != nil {
		t.Fatal(err)
	}
	select {
	case held := <-out:
		if held {
			t.Fatal("the approved call must run in the turn that asked")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the gate did not notice the approval")
	}
}

// TestHoldGate_AnApprovalAnswersOneCall is the one-shot rule without a hash to
// carry it: an approved row is the record of a decision, never a pass. The next
// call of the same shape meets the rule again and waits for its own verdict, so
// an argument-matching release cannot be steered onto the wrong call.
func TestHoldGate_AnApprovalAnswersOneCall(t *testing.T) {
	d := memDB(t)
	srv := &Server{db: d}
	holdRule(t, d, "mcp:delete", auth.EffectHoldBlock)
	addACL(t, d, "google:op", "admin", "atlas", "allow")
	if err := d.PutPendingAction(PendingAction{
		ID: "a1", GroupFolder: "atlas", Tool: "mcp:delete", Args: `{"target":"prod"}`,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := resolvePending(d, "a1", PendingApproved, "google:op", ""); err != nil {
		t.Fatal(err)
	}
	gate := srv.holdGate(turnMCP{folder: "atlas"})
	if _, held := gate("mcp:delete", map[string]any{"target": "prod"}); !held {
		t.Fatal("an approval already given must not admit a second call of the same shape")
	}
	rows, err := d.ListPendingActions("atlas", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("the second call recorded %d rows in total, want its own beside the first", len(rows))
	}
}

// TestHoldGate_ElevatedTurnHasNoGate — an elevated /root turn IS the operator
// acting. Holding their call for their own approval is a deadlock.
func TestHoldGate_ElevatedTurnHasNoGate(t *testing.T) {
	srv := &Server{db: memDB(t)}
	if srv.holdGate(turnMCP{folder: "atlas", elevated: true}) != nil {
		t.Fatal("an elevated turn must not be gated")
	}
}

func TestHoldGate_NoRuleNoOverhead(t *testing.T) {
	srv := &Server{db: memDB(t)}
	gate := srv.holdGate(turnMCP{folder: "atlas"})
	if _, held := gate("mcp:delete", map[string]any{"a": 1}); held {
		t.Fatal("no hold rule must mean inline execution")
	}
}

// TestRetryExhaustedNotice_UsesConfiguredCount: the notice was a literal "3"
// while MAX_TURN_RETRY is configurable, so an operator running 2 was told the
// agent had tried three times.
func TestRetryExhaustedNotice_UsesConfiguredCount(t *testing.T) {
	if got := retryExhaustedNotice(2); !strings.Contains(got, "2 attempts") {
		t.Fatalf("notice = %q, want the configured count", got)
	}
	if got := retryExhaustedNotice(5); !strings.Contains(got, "5 attempts") {
		t.Fatalf("notice = %q, want the configured count", got)
	}
}

// TestResolvePendingAction_ConcurrentDoubleApprove: the pre-UPDATE read is
// advisory — two reviewers pass it together. The write's `status = held`
// condition is what serialises them, so exactly one verdict lands and the other
// reviewer is told someone got there first rather than "approved".
func TestResolvePendingAction_ConcurrentDoubleApprove(t *testing.T) {
	d := memDB(t)
	if err := d.PutPendingAction(PendingAction{
		ID: "a1", GroupFolder: "atlas", Tool: "t",
	}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	oks := 0
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := resolvePending(d, "a1", PendingApproved, "op", ""); err == nil {
				mu.Lock()
				oks++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if oks != 1 {
		t.Fatalf("ResolvePendingAction succeeded %d times for one held row, want 1", oks)
	}
}

// TestListPendingActions_ExpiryFilteredAfterTransform is BUGS J7: filtering on
// the STORED status made `status=expired` return nothing while `status=held`
// returned rows the same call then relabelled expired.
func TestListPendingActions_ExpiryFilteredAfterTransform(t *testing.T) {
	d := memDB(t)
	past := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
	if err := d.PutPendingAction(PendingAction{
		ID: "old", GroupFolder: "atlas", Tool: "t", ExpiresAt: past,
	}); err != nil {
		t.Fatal(err)
	}
	if err := d.PutPendingAction(PendingAction{
		ID: "live", GroupFolder: "atlas", Tool: "t",
	}); err != nil {
		t.Fatal(err)
	}
	expired, err := d.ListPendingActions("atlas", PendingExpired)
	if err != nil {
		t.Fatal(err)
	}
	if len(expired) != 1 || expired[0].ID != "old" {
		t.Fatalf("status=expired returned %+v, want just the expired row", expired)
	}
	held, err := d.ListPendingActions("atlas", PendingHeld)
	if err != nil {
		t.Fatal(err)
	}
	if len(held) != 1 || held[0].ID != "live" {
		t.Fatalf("status=held returned %+v, want only the live row", held)
	}
}

// A held `like` is two lines — the ask, then what `always` would do — and the
// three verdicts as buttons. The chat carries no reaction hint, no command list
// and no dashboard link, even when WEB_HOST is set.
func TestNotifyHeld_LikeIsTwoLinesAndThreeButtons(t *testing.T) {
	d := memDB(t)
	dl := &recDeliverer{}
	srv := &Server{db: d, deliver: dl, webHost: "krons.fiu.wtf"}
	if err := addACLRow(d, core.ACLRow{
		Principal: "folder:atlas", Action: auth.HoldPrefix + "like",
		Scope: "atlas", Effect: "allow",
	}); err != nil {
		t.Fatal(err)
	}
	gate := srv.holdGate(turnMCP{folder: "atlas", chatJID: "tg:1", turnID: "t9"})

	args := map[string]any{"chatJid": "tg:1", "targetId": "156", "reaction": "👀"}
	if _, held := gate("like", args); !held {
		t.Fatal("a matching hold rule must suspend the call")
	}
	id := awaitHeldID(t, d)
	if len(dl.sends) != 1 {
		t.Fatalf("held call must notify once, got %d sends", len(dl.sends))
	}
	want := "⏸ atlas wants to react 👀 to message 156 — ok? (" + id + ")\n" +
		"Always: every like call with chatJid=tg:1,reaction=👀,targetId=156 in atlas runs without asking."
	if got := dl.sends[0].text; got != want {
		t.Errorf("notice =\n%s\nwant\n%s", got, want)
	}
	if n := len(dl.sends[0].options); n != 3 {
		t.Errorf("options = %d, want approve, reject and always", n)
	}
}
