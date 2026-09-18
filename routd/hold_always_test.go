package routd

// Spec 6/26 § Decision 5, the `always` verdict: approve exactly as `yes`, plus
// the skip-approval acl row, in ONE transaction. These tests drive the real
// gate, so what the notice displays and what the writer stores come from the
// same renderer — a display and a write that could disagree is the whole point
// of § "The predicate is part of the verdict".

import (
	"strings"
	"testing"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/core"
)

// holdOnce stands up a gated turn with a bare hold rule on tool and returns the
// server, DB, delivered notices and the id of one held call.
func holdOnce(t *testing.T, tool string, args map[string]any) (*Server, *DB, *recDeliverer, string) {
	t.Helper()
	waitForVerdict(t)
	d := memDB(t)
	dl := &recDeliverer{}
	srv := &Server{db: d, deliver: dl}
	if err := addACLRow(d, core.ACLRow{
		Principal: "folder:atlas", Action: auth.HoldPrefix + tool,
		Scope: "atlas", Effect: "allow",
	}); err != nil {
		t.Fatal(err)
	}
	// A verdict is authorized on the held call's folder, so the reviewer these
	// tests use holds admin there (BUGS L271).
	addACL(t, d, "google:op", "admin", "atlas", "allow")
	gate := srv.holdGate(turnMCP{folder: "atlas", chatJID: "tg:1", turnID: "t1"})
	// The call is STILL WAITING while the test runs its verdict — that is what a
	// hold is. The row has to exist and stay held, so the gate runs where it does
	// in production, on the agent's own goroutine.
	go gate(tool, args)
	id := awaitHeldID(t, d)
	// The NOTICE is a second write, after the row: the gate records the held call
	// and then delivers. Every caller below reads dl.sends[0], so wait for it.
	dl.waitSends(t, 1)
	// End the waiter even when the test leaves the call unanswered.
	t.Cleanup(func() { _, _ = d.expirePendingAction(id) })
	return srv, d, dl, id
}

// always runs the standing verdict through the shared funnel, the way both the
// chat command and the REST face do.
func always(t *testing.T, d *DB, id, reviewer string) error {
	t.Helper()
	_, err := resolveHold(d, id, PendingApproved, reviewer, "", true)
	return err
}

// The acceptance case: `always` approves the call in hand AND writes the rule,
// so the next call of the same shape runs inline while every other shape is
// still held.
func TestAlways_WritesTheRuleAndStopsHolding(t *testing.T) {
	srv, d, _, id := holdOnce(t, "delete", map[string]any{"target": "prod"})
	if err := always(t, d, id, "google:op"); err != nil {
		t.Fatalf("always: %v", err)
	}
	row, ok := pendingActionRow(d, id)
	if !ok || row.Status != PendingApproved {
		t.Fatalf("the call in hand must be approved exactly as yes, got %+v", row)
	}

	gate := srv.holdGate(turnMCP{folder: "atlas", chatJID: "tg:1", turnID: "t2"})
	// The one-shot approval releases the re-issue; the SECOND identical call is
	// the one the standing rule has to carry.
	if _, held := gate("delete", map[string]any{"target": "prod"}); held {
		t.Fatal("the approved re-issue must pass")
	}
	if _, held := gate("delete", map[string]any{"target": "prod"}); held {
		t.Fatal("the standing rule must run the same shape inline")
	}
	if _, held := gate("delete", map[string]any{"target": "staging"}); !held {
		t.Fatal("a different shape must still be held")
	}
}

// The rule that lands is the rule the notice showed, character for character.
// Nothing may build a matcher a second time: one renderer, or a surface can
// display one grant and store another.
func TestAlways_WritesExactlyTheDisplayedMatcher(t *testing.T) {
	_, d, dl, id := holdOnce(t, "create_pr", map[string]any{"repo": "kronael/arizuko", "draft": true})
	notice := dl.sends[0].text
	if !strings.Contains(notice, "/always "+id) {
		t.Fatalf("the notice must offer the standing verdict:\n%s", notice)
	}
	want := "draft=true,repo=kronael/arizuko"
	if !strings.Contains(notice, "create_pr("+want+")") {
		t.Fatalf("the notice must display the exact rule %q:\n%s", want, notice)
	}
	// The two properties that widen the rule past what an operator assumes are
	// stated in words, because neither is visible in the rule text.
	if !strings.Contains(notice, "arguments it does not name are unconstrained") {
		t.Errorf("the notice must say a rule constrains only what it names:\n%s", notice)
	}
	if !strings.Contains(notice, "skips approval only") {
		t.Errorf("the notice must say the mcp: grant still gates the call:\n%s", notice)
	}

	if err := always(t, d, id, "google:op"); err != nil {
		t.Fatalf("always: %v", err)
	}
	rows, err := d.ListACL("")
	if err != nil {
		t.Fatal(err)
	}
	var found int
	for _, r := range rows {
		// The seeded hold rule stays; the verdict adds the skip-approval row
		// beside it, which is the only way an exception can be expressed.
		if r.Action != auth.HoldPrefix+"create_pr" || r.Effect != auth.EffectSkipApproval {
			continue
		}
		found++
		if r.Params != want {
			t.Errorf("stored rule params = %q, notice displayed %q", r.Params, want)
		}
		if r.Principal != "folder:atlas" || r.Scope != "atlas" {
			t.Errorf("stored rule is (%s, %s), want the held call's own agent and folder", r.Principal, r.Scope)
		}
	}
	if found != 1 {
		t.Fatalf("always wrote %d skip-approval rows, want exactly 1", found)
	}
}

// A rule the surface could not state exactly is REFUSED, not approximated. A
// `*` in a value would glob, so the stored rule would match calls the operator
// never saw — a widening they did not agree to.
func TestAlways_RefusesAnUnstatableMatcher(t *testing.T) {
	_, d, dl, id := holdOnce(t, "delete", map[string]any{"target": "prod-*"})
	if notice := dl.sends[0].text; !strings.Contains(notice, "/always is not offered") {
		t.Errorf("the notice must say why no standing verdict is offered:\n%s", notice)
	}
	err := always(t, d, id, "google:op")
	if err == nil {
		t.Fatal("always must refuse a rule it cannot state exactly")
	}
	if !strings.Contains(err.Error(), "standing answer") {
		t.Errorf("the refusal must say what it refused: %v", err)
	}
	rows, listErr := d.ListACL("")
	if listErr != nil {
		t.Fatal(listErr)
	}
	// Only rows a VERDICT wrote: the seeded floor carries skip rows of its own,
	// and they are not what this refusal is about.
	for _, r := range rows {
		if strings.HasPrefix(r.GrantedBy, "always:") {
			t.Fatalf("a refused always still wrote %+v", r)
		}
	}
	// And the refusal is atomic: the call is not left approved without its rule.
	if row, ok := pendingActionRow(d, id); !ok || row.Status != PendingHeld {
		t.Fatalf("a refused always left the call in %q, want still held", row.Status)
	}
}

// A non-scalar argument can never be matched — stringArgs skips it — so the
// notice must name it. Otherwise an operator reads a rule over `repo` and does
// not learn that `files` is free to be anything next time.
func TestAlways_NamesTheArgumentsNoRuleCanConstrain(t *testing.T) {
	_, _, dl, _ := holdOnce(t, "create_pr", map[string]any{
		"repo": "kronael/arizuko", "files": []any{"a.go", "b.go"},
	})
	notice := dl.sends[0].text
	if !strings.Contains(notice, "no rule can constrain files") {
		t.Errorf("the notice must name the unmatchable argument:\n%s", notice)
	}
	if !strings.Contains(notice, "create_pr(repo=kronael/arizuko)") {
		t.Errorf("the rule must still carry the scalar it CAN state:\n%s", notice)
	}
}

// A call with no scalar arguments yields an unnarrowed rule. That is honest —
// there is nothing to constrain — but it is a large grant, so the notice says
// so rather than letting "always" read as "always this one call".
func TestAlways_UnnarrowedRuleSaysSo(t *testing.T) {
	_, _, dl, _ := holdOnce(t, "wipe", map[string]any{})
	if notice := dl.sends[0].text; !strings.Contains(notice, "EVERY call to this tool will run") {
		t.Errorf("an unnarrowed rule must be described as one:\n%s", notice)
	}
}

// `never` does not ship. Undoing it means deleting the tool deny it also
// writes, and acl rows carry no verdict identity, so a revoke cannot tell that
// deny from standing policy that predates it (spec 6/26 § "Revocation needs
// provenance"). No surface may offer it until that is designed.
func TestNever_IsNotOffered(t *testing.T) {
	_, _, dl, id := holdOnce(t, "delete", map[string]any{"target": "prod"})
	if notice := dl.sends[0].text; strings.Contains(notice, "/never") {
		t.Errorf("the notice offers a verdict that cannot be revoked:\n%s", notice)
	}
	l := &Loop{}
	if handled := l.handleCommand("tg:1", core.Message{
		Content: "/never " + id, Sender: "google:op",
	}, "atlas"); handled {
		t.Error("/never is a live command; it must not exist until revocation has provenance")
	}
}

// awaitNotice waits for the hold notice the gate delivers after it records the
// row. sentMsgs takes the deliverer's lock, so the wait is also the safe read.
func awaitNotice(t *testing.T, dl *recDeliverer) {
	t.Helper()
	dl.waitSends(t, 1)
}
