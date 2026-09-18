package routd

import (
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/core"
)

// holdRule installs one hold rule on folder atlas.
func holdRule(t *testing.T, d *DB, tool, effect string) {
	t.Helper()
	if err := addACLRow(d, core.ACLRow{
		Principal: "folder:atlas", Action: auth.HoldPrefix + tool,
		Scope: "atlas", Effect: effect,
	}); err != nil {
		t.Fatal(err)
	}
}

// waitForVerdict makes a blocking hold wait, where TestMain makes it give up at
// once.
func waitForVerdict(t *testing.T) {
	t.Helper()
	HoldWait = 10 * time.Second
	t.Cleanup(func() { HoldWait = 0 })
}

// awaitHeldID waits for the gate goroutine to record its row and returns its id.
func awaitHeldID(t *testing.T, d *DB) string {
	t.Helper()
	var id string
	awaitCond(t, "the gate never recorded a held row", func() bool {
		rows, err := d.ListPendingActions("atlas", "")
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) == 1 {
			id = rows[0].ID
			return true
		}
		return false
	})
	return id
}

// TestHoldGate_BlockingRunsTheApprovedCall is the whole contract: the agent's
// call WAITS inside the turn, and the approval that lands while it waits runs it
// there. `held=false` is the gate telling ipc to run the call, so the human's yes
// takes effect in the turn that asked and the agent reports the real result.
func TestHoldGate_BlockingRunsTheApprovedCall(t *testing.T) {
	waitForVerdict(t)

	d := memDB(t)
	srv := &Server{db: d}
	holdRule(t, d, "mcp:delete", auth.EffectHoldBlock)
	gate := srv.holdGate(turnMCP{folder: "atlas", turnID: "t1"})

	type verdict struct {
		refusal string
		held    bool
	}
	out := make(chan verdict, 1)
	go func() {
		refusal, held := gate("mcp:delete", map[string]any{"target": "prod"})
		out <- verdict{refusal, held}
	}()

	id := awaitHeldID(t, d)
	if _, err := resolvePending(d, id, PendingApproved, "google:op", ""); err != nil {
		t.Fatal(err)
	}
	select {
	case v := <-out:
		if v.held {
			t.Fatalf("an approval during the wait must run the call inline, got refusal %q", v.refusal)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the gate did not notice the approval")
	}
	row, _ := pendingActionRow(d, id)
	if row.Status != PendingApproved {
		t.Fatalf("the verdict is the row's last word, status is %q", row.Status)
	}
}

// TestHoldGate_WaitsUntilAVerdictArrives — a held call does not come back on its
// own. Returning before a human answers is what let an agent tell a user an
// action was taken that nobody had approved.
func TestHoldGate_WaitsUntilAVerdictArrives(t *testing.T) {
	waitForVerdict(t)

	d := memDB(t)
	srv := &Server{db: d}
	holdRule(t, d, "mcp:delete", auth.EffectHoldBlock)
	gate := srv.holdGate(turnMCP{folder: "atlas", turnID: "t1"})

	out := make(chan bool, 1)
	go func() {
		_, held := gate("mcp:delete", map[string]any{"target": "prod"})
		out <- held
	}()

	id := awaitHeldID(t, d)
	select {
	case <-out:
		t.Fatal("the gate answered before any human did")
	case <-time.After(200 * time.Millisecond):
	}
	if _, err := resolvePending(d, id, PendingApproved, "google:op", ""); err != nil {
		t.Fatal(err)
	}
	select {
	case held := <-out:
		if held {
			t.Fatal("the approved call must run")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the gate did not notice the approval")
	}
}

// TestHoldGate_RejectionReachesTheAgentWithItsNote — a rejection ends the wait
// too. The call must NOT run, and the agent is told WHY in the same breath: a
// refusal with no reason reaches the user as "it did not work", which is the
// failure the reviewer's note exists to prevent.
func TestHoldGate_RejectionReachesTheAgentWithItsNote(t *testing.T) {
	waitForVerdict(t)

	d := memDB(t)
	srv := &Server{db: d}
	holdRule(t, d, "mcp:delete", auth.EffectHoldBlock)
	gate := srv.holdGate(turnMCP{folder: "atlas", turnID: "t1"})

	type answer struct {
		refusal string
		held    bool
	}
	out := make(chan answer, 1)
	go func() {
		refusal, held := gate("mcp:delete", map[string]any{"target": "prod"})
		out <- answer{refusal, held}
	}()

	id := awaitHeldID(t, d)
	if _, err := resolvePending(d, id, PendingRejected, "google:op", "prod is frozen"); err != nil {
		t.Fatal(err)
	}
	select {
	case a := <-out:
		if !a.held {
			t.Fatal("a rejected call must never run")
		}
		for _, want := range []string{"NOT RUN", "rejected", "prod is frozen"} {
			if !strings.Contains(a.refusal, want) {
				t.Errorf("the agent was told %q, which does not carry %q", a.refusal, want)
			}
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the gate did not notice the rejection")
	}
}

// TestAwaitVerdict_TimeoutExpiresTheRow — when the waiter gives up, the row is
// CLOSED. Nobody is suspended on it any more, so a verdict typed later could
// never reach the agent; a row left `held` would take that verdict and look
// accepted. The agent is told nobody answered.
func TestAwaitVerdict_TimeoutExpiresTheRow(t *testing.T) {
	d := memDB(t)
	srv := &Server{db: d}
	holdRule(t, d, "mcp:delete", auth.EffectHoldBlock)
	gate := srv.holdGate(turnMCP{folder: "atlas", turnID: "t1"})

	refusal, held := gate("mcp:delete", map[string]any{"target": "prod"})
	if !held {
		t.Fatal("a hold rule suspends the call")
	}
	if !strings.Contains(refusal, "nobody answered in time") {
		t.Errorf("the agent was told %q, which does not say nobody answered", refusal)
	}
	rows, err := d.ListPendingActions("atlas", "")
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows = %v, err = %v; want the one call that timed out", rows, err)
	}
	if rows[0].Status != PendingExpired {
		t.Fatalf("a call nobody answered is %q, want expired", rows[0].Status)
	}
	// And the closed row refuses a late verdict rather than looking accepted.
	if _, err := resolvePending(d, rows[0].ID, PendingApproved, "google:op", ""); err == nil {
		t.Fatal("a verdict on an expired call must be refused, not recorded")
	}
}

// TestResolveHold_WritesNoTrigger — the verdict tells nobody to run anything.
// The agent is suspended on the row, so the waiter is the only runner; a message
// written here would make the agent run an approved call a second time.
func TestResolveHold_WritesNoTrigger(t *testing.T) {
	d := memDB(t)
	addACL(t, d, "google:op", "admin", "atlas", "allow")
	if err := d.PutPendingAction(PendingAction{
		ID: "id1", GroupFolder: "atlas", CallerAgent: "folder:atlas",
		Tool: "mcp:delete", Args: `{"target":"prod"}`, ChatJID: "tg:1",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveHold(d, "id1", PendingApproved, "google:op", "", false); err != nil {
		t.Fatal(err)
	}
	if msgs := chatMessages(t, d, "tg:1"); len(msgs) != 0 {
		t.Fatalf("the verdict wrote %d message(s) into the held chat, want none", len(msgs))
	}
}

// TestNotifyHeld_OffersTheVerdictsAsButtons — a button's data IS the command it
// replaces, which is what makes the press and the typed command one path with
// one operator gate. A button carrying a private token would be a second
// authorization path handed to every reader of the chat.
func TestNotifyHeld_OffersTheVerdictsAsButtons(t *testing.T) {
	d := memDB(t)
	dl := &recDeliverer{}
	srv := &Server{db: d, deliver: dl}
	holdRule(t, d, "mcp:delete", auth.EffectHoldBlock)
	gate := srv.holdGate(turnMCP{folder: "atlas", chatJID: "tg:1", turnID: "t9"})

	_, held := gate("mcp:delete", map[string]any{"target": "prod"})
	if !held {
		t.Fatal("a matching hold rule must suspend the call")
	}
	id := awaitHeldID(t, d)
	if len(dl.sends) != 1 {
		t.Fatalf("held call must notify once, got %d sends", len(dl.sends))
	}
	var got []string
	for _, o := range dl.sends[0].options {
		got = append(got, o.Data)
	}
	want := []string{"/approve " + id, "/reject " + id, "/always " + id}
	if len(got) != len(want) {
		t.Fatalf("options = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("option %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestNotifyHeld_NoAlwaysButtonWithoutAnExactRule — `always` writes a rule, and
// the surface refuses to offer a rule it could not state. The text already says
// why; the button must not contradict it by being there.
func TestNotifyHeld_NoAlwaysButtonWithoutAnExactRule(t *testing.T) {
	opts := verdictOptions(PendingAction{ID: "ab12", MatcherError: "cannot state this call"})
	for _, o := range opts {
		if o.Data == "/always ab12" {
			t.Fatal("always must not be offered when no exact rule can be written")
		}
	}
	if len(opts) != 2 {
		t.Fatalf("approve and reject stay on offer, got %d options", len(opts))
	}
}

// TestAwaitVerdict_VerdictInTheClosingInstantWins — the expiry is conditioned on
// the row still being held, so losing that write means a human answered exactly
// as the wait ended. The ROW is the record: the agent must be told what the
// person decided, not that nobody answered, or the queue shows an approval the
// agent reported as a timeout.
func TestAwaitVerdict_VerdictInTheClosingInstantWins(t *testing.T) {
	d := memDB(t)
	srv := &Server{db: d}
	addACL(t, d, "google:op", "admin", "atlas", "allow")
	if err := d.PutPendingAction(PendingAction{
		ID: "a1", GroupFolder: "atlas", Tool: "mcp:delete", Args: `{"target":"prod"}`,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := resolvePending(d, "a1", PendingApproved, "google:op", ""); err != nil {
		t.Fatal(err)
	}
	// HoldWait is 0 here (TestMain), so the wait ends before any poll — the same
	// position the waiter is in when a verdict lands on its last pass.
	refusal, held := srv.awaitVerdict("a1", "mcp:delete")
	if held {
		t.Fatalf("the approved call must run, the agent was told %q", refusal)
	}
	if row, _ := pendingActionRow(d, "a1"); row.Status != PendingApproved {
		t.Fatalf("status = %q, want approved — the expiry must not overwrite a verdict", row.Status)
	}
}
