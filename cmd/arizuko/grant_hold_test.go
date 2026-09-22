package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/store"
)

// The `always` verdict writes THIS row (routd/pending_actions.go skipApprovalRow):
// the `hold:` action, effect deny, and the matcher the notice displayed. Every
// field but the predicate is part of store.aclKey, so a revoke that names fewer
// of them addresses a different item.
const (
	holdAgent   = "folder:atlas/eng"
	holdFolder  = "atlas/eng"
	holdTool    = "reply"
	holdMatcher = "to=slack:C1"
)

// seedHold puts the hold RULE in place — `hold:mcp:<tool>` at EffectHoldBlock —
// so CheckHold has something to answer yes to, and the skip row below has
// something to except.
func seedHold(t *testing.T, s *store.Store) {
	t.Helper()
	if err := s.AddACLRow(core.ACLRow{
		Principal: holdAgent, Action: auth.HoldPrefix + holdTool, Scope: holdFolder,
		Effect: auth.EffectHoldBlock, GrantedBy: "test",
	}); err != nil {
		t.Fatalf("seed the hold rule: %v", err)
	}
}

// held asks the evaluator the question the agent's turn asks: must this call
// wait for a human?
func held(s *store.Store) bool {
	return auth.CheckHold(s, auth.Caller{Principal: holdAgent}, holdTool, holdFolder,
		map[string]string{"to": "slack:C1"})
}

// TestUngrantRemovesTheRowAlwaysWrote is L138 itself. `runUngrant` pinned
// action=admin, effect=allow and params="", so it addressed exactly one of the
// six shapes an acl row can take — and never the one `/always` writes. An
// operator on the host could not undo a skip-approval rule, which is precisely
// the surface that matters when the dashboard is what is broken.
func TestUngrantRemovesTheRowAlwaysWrote(t *testing.T) {
	s := newMem(t)
	seedHold(t, s)
	if !held(s) {
		t.Fatal("the hold rule does not hold the call, so this test asserts nothing")
	}

	skip := core.ACLRow{
		Principal: holdAgent, Action: auth.HoldPrefix + holdTool, Scope: holdFolder,
		Effect: auth.EffectSkipApproval, Params: holdMatcher, GrantedBy: "always:operator",
	}
	if err := s.AddACLRow(skip); err != nil {
		t.Fatalf("write the row /always writes: %v", err)
	}
	if held(s) {
		t.Fatal("the skip-approval row did not take effect")
	}

	// The shape L138 measured: principal and scope only, so the defaults pin
	// action=admin, effect=allow, params="". It must NOT reach the skip row.
	var out bytes.Buffer
	if err := runUngrant(s, core.ACLRow{Principal: holdAgent, Scope: holdFolder}, &out); err != nil {
		t.Fatalf("the old ungrant shape errored: %v", err)
	}
	if held(s) {
		t.Fatal("a principal+scope ungrant removed the skip-approval row; it addresses " +
			"action=admin effect=allow params='' and must not reach a hold rule")
	}

	// The full coordinate does reach it.
	out.Reset()
	if err := runUngrant(s, core.ACLRow{
		Principal: holdAgent, Action: auth.HoldPrefix + holdTool, Scope: holdFolder,
		Effect: auth.EffectSkipApproval, Params: holdMatcher,
	}, &out); err != nil {
		t.Fatalf("ungrant the skip-approval row: %v", err)
	}
	if !held(s) {
		t.Fatal("the skip-approval row survived an ungrant naming its whole coordinate")
	}
	if !strings.Contains(out.String(), holdMatcher) {
		t.Errorf("the ungrant line hides the matcher it removed: %q", out.String())
	}
}

// TestGrantWritesAHoldRule: grantableAction accepted mcp:, shell:env: and
// ext:, so `hold:mcp:<tool>` was untypeable and the CLI could not install a
// skip-approval rule OR the hold it excepts — the grant_action_needs_a_door
// shape, where a namespace is unreachable until the allowlist learns it.
func TestGrantWritesAHoldRule(t *testing.T) {
	s := newMem(t)
	seedHold(t, s)
	if !held(s) {
		t.Fatal("the hold rule does not hold the call, so this test asserts nothing")
	}

	var out bytes.Buffer
	if err := runGrant(s, core.ACLRow{
		Principal: holdAgent, Action: auth.HoldPrefix + holdTool, Scope: holdFolder,
		Effect: store.EffectDeny, Params: holdMatcher,
	}, &out); err != nil {
		t.Fatalf("grant a skip-approval rule: %v", err)
	}
	if held(s) {
		t.Fatal("the granted skip-approval rule does not skip the hold")
	}
	if !strings.Contains(out.String(), holdMatcher) {
		t.Errorf("the grant line hides the matcher it wrote: %q", out.String())
	}

	// The narrowing is real: a call with a different argument still waits.
	if !auth.CheckHold(s, auth.Caller{Principal: holdAgent}, holdTool, holdFolder,
		map[string]string{"to": "slack:OTHER"}) {
		t.Fatal("the rule skipped a call its matcher does not name")
	}
}

// TestUngrantRejectsUnknownEffect is the half no other layer guards.
// store.aclPut validates the effect on the WRITE, so a misspelt `grant` already
// fails loud there. RemoveACLRow validates NOTHING, and a delete of a
// coordinate nobody wrote succeeds — so without the check in checkGrantRow an
// `ungrant --effect denny` reports success while the rule it named stands.
func TestUngrantRejectsUnknownEffect(t *testing.T) {
	s := newMem(t)
	seedHold(t, s)
	skip := core.ACLRow{
		Principal: holdAgent, Action: auth.HoldPrefix + holdTool, Scope: holdFolder,
		Effect: auth.EffectSkipApproval, Params: holdMatcher, GrantedBy: "always:operator",
	}
	if err := s.AddACLRow(skip); err != nil {
		t.Fatalf("write the row the revoke will miss: %v", err)
	}

	var out bytes.Buffer
	typo := skip
	typo.Effect = "denny"
	err := runUngrant(s, typo, &out)
	if err == nil {
		t.Fatalf("ungrant accepted effect %q and answered success; the rule it named "+
			"is still there and the operator has been told otherwise", typo.Effect)
	}
	if !strings.Contains(err.Error(), "unknown effect") {
		t.Errorf("the refusal does not name the effect: %v", err)
	}
	if held(s) {
		t.Fatal("the typo'd ungrant removed the row anyway")
	}
}

// TestGrantsListsParams: the listing printed five of the six key fields, so two
// narrowings of one tool showed as one indistinguishable line and an operator
// could not type the ungrant that removes either.
func TestGrantsListsParams(t *testing.T) {
	s := newMem(t)
	var sink bytes.Buffer
	if err := runGrant(s, core.ACLRow{
		Principal: holdAgent, Action: auth.HoldPrefix + holdTool, Scope: holdFolder,
		Effect: store.EffectDeny, Params: holdMatcher,
	}, &sink); err != nil {
		t.Fatalf("grant: %v", err)
	}

	var out bytes.Buffer
	if err := runGrants(s, holdAgent, &out); err != nil {
		t.Fatalf("runGrants: %v", err)
	}
	if !strings.Contains(out.String(), "PARAMS") {
		t.Errorf("the listing has no PARAMS column: %q", out.String())
	}
	if !strings.Contains(out.String(), holdMatcher) {
		t.Errorf("the listing hides the matcher that is part of the row's key: %q", out.String())
	}
}

// TestUngrantOfAGlobRuleKeepsTheOperatorRole: `ungrant <sub> **` took the
// operator role whatever action it carried, while `grant` only ever created the
// role for action=admin. Revoking one `**` rule therefore stripped the role too.
func TestUngrantOfAGlobRuleKeepsTheOperatorRole(t *testing.T) {
	s := newMem(t)
	var out bytes.Buffer
	if err := runGrant(s, core.ACLRow{Principal: "github:1", Scope: "**"}, &out); err != nil {
		t.Fatalf("operator grant: %v", err)
	}
	if err := runGrant(s, core.ACLRow{Principal: "github:1", Scope: "**", Action: "egress"}, &out); err != nil {
		t.Fatalf("glob egress grant: %v", err)
	}

	if err := runUngrant(s, core.ACLRow{Principal: "github:1", Scope: "**", Action: "egress"}, &out); err != nil {
		t.Fatalf("ungrant the glob rule: %v", err)
	}
	if !hasOperator(s, "github:1") {
		t.Fatal("revoking a `**` rule took the operator role with it")
	}

	// The operator row itself still goes, and by the same coordinate.
	if err := runUngrant(s, core.ACLRow{Principal: "github:1", Scope: "**"}, &out); err != nil {
		t.Fatalf("ungrant the operator role: %v", err)
	}
	if hasOperator(s, "github:1") {
		t.Fatal("the operator role survived its own ungrant")
	}
}

func hasOperator(s *store.Store, sub string) bool {
	for _, a := range s.Ancestors(sub) {
		if a == "role:operator" {
			return true
		}
	}
	return false
}

// TestParseGrantArgsReadsTheWholeCoordinate pins the words an operator types.
// flexParse lets the flags sit anywhere, which is what makes the action stay a
// positional third word and existing muscle memory keep working.
func TestParseGrantArgsReadsTheWholeCoordinate(t *testing.T) {
	row, err := parseGrantArgs("grant", []string{
		holdAgent, "--params", holdMatcher, holdFolder,
		auth.HoldPrefix + holdTool, "--effect", "deny",
	})
	if err != nil {
		t.Fatalf("parseGrantArgs: %v", err)
	}
	want := core.ACLRow{
		Principal: holdAgent, Scope: holdFolder, Action: auth.HoldPrefix + holdTool,
		Effect: "deny", Params: holdMatcher,
	}
	if row != want {
		t.Errorf("parseGrantArgs = %+v, want %+v", row, want)
	}

	// Two positionals is the unchanged call, and the defaults finish the row.
	row, err = parseGrantArgs("ungrant", []string{"github:1", "alice"})
	if err != nil {
		t.Fatalf("parseGrantArgs two positionals: %v", err)
	}
	if got := store.GrantDefaults(row); got.Action != "admin" || got.Effect != store.EffectAllow {
		t.Errorf("defaults = %+v, want action=admin effect=allow", got)
	}

	if _, err := parseGrantArgs("grant", []string{"only-one"}); err == nil {
		t.Error("a grant naming no scope was accepted")
	}
	if _, err := parseGrantArgs("grant", []string{"a", "b", "c", "d"}); err == nil {
		t.Error("a fourth positional was accepted; predicate has no flag by design")
	}
}
