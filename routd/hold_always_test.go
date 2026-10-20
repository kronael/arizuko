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

// The acceptance case: `always` approves the call in hand AND writes a rule
// that pins the kind of call — a like's reaction, not the message it names — so
// a later like with that reaction runs inline while another reaction is held.
func TestAlways_PinsTheKindOfCall(t *testing.T) {
	srv, d, _, id := holdOnce(t, "like", map[string]any{
		"chatJid": "tg:1", "targetId": "156", "reaction": "👀"})
	if err := always(t, d, id, "google:op"); err != nil {
		t.Fatalf("always: %v", err)
	}
	row, ok := pendingActionRow(d, id)
	if !ok || row.Status != PendingApproved {
		t.Fatalf("the call in hand must be approved exactly as yes, got %+v", row)
	}
	rows, err := d.ListACL("")
	if err != nil {
		t.Fatal(err)
	}
	var params []string
	for _, r := range rows {
		if strings.HasPrefix(r.GrantedBy, "always:") {
			params = append(params, r.Params)
		}
	}
	if len(params) != 1 || params[0] != "reaction=👀" {
		t.Fatalf("always wrote params %q, want exactly [reaction=👀]", params)
	}

	gate := srv.holdGate(turnMCP{folder: "atlas", chatJID: "tg:1", turnID: "t2"})
	if _, held := gate("like", map[string]any{
		"chatJid": "tg:1", "targetId": "157", "reaction": "👀"}); held {
		t.Fatal("a later like with that reaction and another message must run")
	}
	if _, held := gate("like", map[string]any{
		"chatJid": "tg:1", "targetId": "158", "reaction": "❤️"}); !held {
		t.Fatal("a like with another reaction must still be held")
	}
}

// The rule that lands is the rule the notice showed, character for character.
// Nothing may build a matcher a second time: one renderer, or a surface can
// display one grant and store another.
func TestAlways_WritesExactlyTheDisplayedMatcher(t *testing.T) {
	_, d, dl, id := holdOnce(t, "like", map[string]any{
		"chatJid": "tg:1", "targetId": "156", "reaction": "👀"})
	notice := dl.sends[0].text
	want := "reaction=👀"
	line := "Always: every like call with " + want + " in atlas runs without asking."
	if !strings.HasSuffix(notice, line) {
		t.Fatalf("the notice must end with the exact rule %q:\n%s", line, notice)
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
		if r.Action != auth.HoldPrefix+"like" || !strings.HasPrefix(r.GrantedBy, "always:") {
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
	_, d, dl, id := holdOnce(t, "like", map[string]any{
		"chatJid": "tg:1", "targetId": "156", "reaction": "*"})
	if notice := dl.sends[0].text; !strings.Contains(notice, "Always is not offered") {
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

// A tool that declares no always keys gets a tool-wide row: no params, every
// call of the tool in the folder runs. The Always line says so in those words.
func TestAlways_ToolWithoutKeysWritesTheToolWideRow(t *testing.T) {
	srv, d, dl, id := holdOnce(t, "create_pr", map[string]any{"repo": "kronael/arizuko"})
	line := "Always: every create_pr call in atlas runs without asking."
	if notice := dl.sends[0].text; !strings.HasSuffix(notice, line) {
		t.Errorf("the notice must end with %q:\n%s", line, notice)
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
		if r.Action == auth.HoldPrefix+"create_pr" && r.Effect == auth.EffectSkipApproval {
			found++
			if r.Params != "" {
				t.Errorf("tool-wide row carries params %q, want none", r.Params)
			}
		}
	}
	if found != 1 {
		t.Fatalf("always wrote %d tool-wide rows, want 1", found)
	}
	gate := srv.holdGate(turnMCP{folder: "atlas", chatJID: "tg:1", turnID: "t2"})
	if _, held := gate("create_pr", map[string]any{"repo": "other/repo"}); held {
		t.Fatal("any call of the tool must run after a tool-wide always")
	}
}

// `never` does not ship. Undoing it means deleting the tool deny it also
// writes, and acl rows carry no verdict identity, so a revoke cannot tell that
// deny from standing policy that predates it (spec 6/26-personal-mode §"The
// mechanics behind `always`"). No surface may offer it until that is designed.
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

// Each verdict is acknowledged in one line; an /always names the rule and where
// to revoke it, and quotes no remove_acl call.
func TestResolveHold_AcknowledgesInOneLine(t *testing.T) {
	cases := []struct {
		verdict  string
		standing bool
		want     func(id string) string
	}{
		{PendingRejected, false, func(id string) string { return "⛔ rejected (" + id + ")." }},
		{PendingApproved, false, func(id string) string { return "✅ ok (" + id + ") — the agent runs it now." }},
		{PendingApproved, true, func(id string) string {
			return "✅ ok (" + id + "). Always: every create_pr call in atlas " +
				"runs without asking. Undo: revoke the hold:mcp:create_pr skip row on the grants page."
		}},
	}
	for _, c := range cases {
		_, d, _, id := holdOnce(t, "create_pr", map[string]any{"repo": "kronael/arizuko"})
		dl := &recDeliverer{}
		l := &Loop{db: d, deliver: dl}
		l.cmdResolveHold("tg:1", "atlas", id, "google:op", c.verdict, c.standing)
		got := dl.sentMsgs()
		if len(got) != 1 || got[0].text != c.want(id) {
			t.Errorf("%s standing=%v: ack = %+v, want %q", c.verdict, c.standing, got, c.want(id))
		}
	}
}
