package store

import (
	"strings"
	"testing"

	"github.com/kronael/arizuko/core"
)

func TestACL_CRUD(t *testing.T) {
	s := openMem(t)
	row := core.ACLRow{
		Principal: "google:alice",
		Action:    "admin",
		Scope:     "atlas/**",
		Effect:    "allow",
	}
	if err := s.AddACLRow(row); err != nil {
		t.Fatal(err)
	}
	// Idempotent.
	if err := s.AddACLRow(row); err != nil {
		t.Fatal(err)
	}
	got := mustACL(s.ACLRowsFor([]string{"google:alice"}))
	if len(got) != 1 {
		t.Fatalf("ACLRowsFor: %d rows %+v", len(got), got)
	}
	if got[0].Action != "admin" || got[0].Scope != "atlas/**" {
		t.Fatalf("row contents: %+v", got[0])
	}
	if err := s.RemoveACLRow(row); err != nil {
		t.Fatal(err)
	}
	if got := mustACL(s.ACLRowsFor([]string{"google:alice"})); len(got) != 0 {
		t.Fatalf("after remove: %v", got)
	}
}

// spec 5/33: grant_option round-trips; default is false (WITHOUT grant option).
func TestACL_GrantOption(t *testing.T) {
	s := openMem(t)
	if err := s.AddACLRow(core.ACLRow{
		Principal: "role:owner", Action: "admin", Scope: "acme/**", GrantOption: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddACLRow(core.ACLRow{
		Principal: "role:member", Action: "mcp:send", Scope: "acme/eng",
	}); err != nil {
		t.Fatal(err)
	}
	owner := mustACL(s.ACLRowsFor([]string{"role:owner"}))
	if len(owner) != 1 || !owner[0].GrantOption {
		t.Fatalf("owner grant_option not persisted: %+v", owner)
	}
	member := mustACL(s.ACLRowsFor([]string{"role:member"}))
	if len(member) != 1 || member[0].GrantOption {
		t.Fatalf("member default grant_option must be false: %+v", member)
	}
}

func TestACL_DefaultEffectAllow(t *testing.T) {
	s := openMem(t)
	row := core.ACLRow{
		Principal: "p",
		Action:    "interact",
		Scope:     "f",
	}
	if err := s.AddACLRow(row); err != nil {
		t.Fatal(err)
	}
	got := mustACL(s.ACLRowsFor([]string{"p"}))
	if len(got) != 1 || got[0].Effect != "allow" {
		t.Fatalf("default effect: %+v", got)
	}
}

func TestACL_WildcardRows(t *testing.T) {
	s := openMem(t)
	exact := core.ACLRow{Principal: "google:alice", Action: "admin", Scope: "f"}
	wild := core.ACLRow{Principal: "discord:user/*", Action: "interact", Scope: "f"}
	if err := s.AddACLRow(exact); err != nil {
		t.Fatal(err)
	}
	if err := s.AddACLRow(wild); err != nil {
		t.Fatal(err)
	}
	w := mustACL(s.ACLWildcardRows())
	if len(w) != 1 || w[0].Principal != "discord:user/*" {
		t.Fatalf("wildcards: %+v", w)
	}
	// Exact-only path should not return wildcards.
	got := mustACL(s.ACLRowsFor([]string{"google:alice"}))
	if len(got) != 1 || got[0].Principal != "google:alice" {
		t.Fatalf("exact-only: %+v", got)
	}
}

func TestACL_DenyCoexistsWithAllow(t *testing.T) {
	s := openMem(t)
	allow := core.ACLRow{Principal: "p", Action: "admin", Scope: "f", Effect: "allow"}
	deny := core.ACLRow{Principal: "p", Action: "admin", Scope: "f", Effect: "deny"}
	if err := s.AddACLRow(allow); err != nil {
		t.Fatal(err)
	}
	if err := s.AddACLRow(deny); err != nil {
		t.Fatal(err)
	}
	got := mustACL(s.ACLRowsFor([]string{"p"}))
	if len(got) != 2 {
		t.Fatalf("expected 2 rows, got %d: %+v", len(got), got)
	}
}

// mustACL unwraps an ACL read in a test that is not about read failures. It
// takes the call's two results directly, so a site reads as one expression;
// store/failclosed_test.go is where the error path itself is asserted.
func mustACL(rows []core.ACLRow, err error) []core.ACLRow {
	if err != nil {
		panic("acl read: " + err.Error())
	}
	return rows
}

// TestEffectAllowed_OnlyTwoSpellings — an acl row's effect decides whether the
// row grants or refuses, and the evaluator reads every value that is not exactly
// `deny` as ALLOW. Anything else is a grant nobody meant to write, so the writer
// refuses it (BUGS L272).
func TestEffectAllowed_OnlyTwoSpellings(t *testing.T) {
	for _, c := range []struct {
		effect string
		want   bool
	}{
		{EffectAllow, true},
		{EffectDeny, true},
		{"async", false},
		{"denny", false},
		{"maybe", false},
		{"", false},
	} {
		if got := EffectAllowed(c.effect); got != c.want {
			t.Errorf("EffectAllowed(%q) = %v, want %v", c.effect, got, c.want)
		}
	}
}

// Two folder creators wrote two different admin scopes: a bare `eng` and
// `eng/**`. The bare one reads as admin of the folder and 403 on every child,
// because matchSegments ends on len(in) == 0 (BUGS L206). One renderer now
// makes the shape, and this says which shape it is.
func TestAdminGrantWriteCoversTheFolderAndItsSubtree(t *testing.T) {
	w, err := AdminGrantWrite("alice@x", "eng", "dashd")
	if err != nil {
		t.Fatalf("AdminGrantWrite: %v", err)
	}
	// The scope rides the coordinate, so the key is where it is asserted.
	sort := w.Key.Sort
	if !strings.Contains(sort, "eng/**") {
		t.Fatalf("sort key = %q, want it to carry eng/** — a bare folder scope refuses every child", sort)
	}
	if !strings.Contains(sort, "admin") {
		t.Errorf("sort key = %q, want the admin action", sort)
	}
	if !strings.Contains(sort, "alice@x") {
		t.Errorf("sort key = %q, want the principal", sort)
	}
}

// A predicate on a shell:env: row can only ever mean "never": ShellEnvKeys
// resolves once per dispatch keyed on the folder and carries no claims, and
// predicateMatches answers true only for an empty predicate. So
// `predicate=platform=telegram`, which reads like "only in Telegram", revokes
// the key on EVERY channel and nothing logs a denial, because nothing was
// denied (BUGS L134). The write refuses it instead.
func TestACLRefusesAPredicateOnAShellEnvGrant(t *testing.T) {
	_, err := ACLGrantWrite(core.ACLRow{
		Principal: "folder:atlas", Action: ShellEnvPrefix + "GITHUB_TOKEN",
		Scope: "atlas", Effect: EffectAllow, Predicate: "platform=telegram",
		GrantedBy: "test",
	})
	if err == nil {
		t.Fatal("an impossible shell:env predicate was accepted; it would revoke the key silently")
	}
	for _, want := range []string{"can never match", "platform=telegram", "exact folder"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal %q does not carry %q — the operator cannot act on it", err.Error(), want)
		}
	}
}

// The same grant with NO predicate is the shape that works, and must still be
// writable — a guard that refused both would take the capability away.
func TestACLAcceptsAShellEnvGrantWithNoPredicate(t *testing.T) {
	if _, err := ACLGrantWrite(core.ACLRow{
		Principal: "folder:atlas", Action: ShellEnvPrefix + "GITHUB_TOKEN",
		Scope: "atlas", Effect: EffectAllow, GrantedBy: "test",
	}); err != nil {
		t.Fatalf("the working shape was refused: %v", err)
	}
}

// A predicate on any OTHER namespace stays legal. The WRITE is what this pins,
// and the guard is deliberately scoped to shell:env: rather than to predicates
// in general.
//
// It is not evidence that a hold: predicate works. auth.CheckHold forwards
// caller.Claims, but routd asks every hold question as
// auth.Caller{Principal: sub} and fills none, so on the live path the row is
// inert and the call runs UNHELD —
// auth.TestCheckHold_PredicateIsInertOnTheLiveCaller measures it. Whether the
// caller should carry the trigger's platform is the open half of BUGS L134;
// until that is answered this write stays legal on purpose.
func TestACLAllowsAPredicateOnAHoldGrant(t *testing.T) {
	if _, err := ACLGrantWrite(core.ACLRow{
		Principal: "folder:atlas", Action: HoldPrefix + "send",
		Scope: "atlas", Effect: EffectAllow, Predicate: "platform=telegram",
		GrantedBy: "test",
	}); err != nil {
		t.Fatalf("a hold: predicate was refused, but its evaluator reads claims: %v", err)
	}
}

// The grant writer's defaults and the store writer's are two functions on
// purpose, and the difference is load-bearing: GrantDefaults fills the ACTION,
// aclDefaults does not. A row reaching AddACLRow with no action is REFUSED by
// the coordinate (the test below), so merging the two would turn a loud
// refusal into a silent `admin` grant for every caller of AddACLRow.
//
// This pins the difference so neither drifts into the other by accident — the
// widening would be silent, because an empty action and `admin` write rows that
// both look reasonable in the table.
func TestGrantDefaultsFillsTheActionAndTheRowWriterDoesNot(t *testing.T) {
	bare := core.ACLRow{Principal: "github:bob", Scope: "main/**"}

	granted := GrantDefaults(bare)
	if granted.Action != "admin" {
		t.Errorf("GrantDefaults left the action %q; the grant and its ungrant would "+
			"address different coordinates (BUGS L138)", granted.Action)
	}
	if granted.Effect != EffectAllow {
		t.Errorf("GrantDefaults effect = %q, want %q", granted.Effect, EffectAllow)
	}

	stored := aclDefaults(bare)
	if stored.Action != "" {
		t.Errorf("aclDefaults filled the action with %q — that widens every caller "+
			"of AddACLRow, and BUGS L345 is open precisely because it is a decision",
			stored.Action)
	}
	if stored.Effect != EffectAllow {
		t.Errorf("aclDefaults effect = %q, want %q", stored.Effect, EffectAllow)
	}
}

// A grant naming no action grants nothing: actionCovers answers false for every
// request against an empty row action. The coordinate refuses it rather than
// storing a row that reads like a grant and is not one.
func TestAGrantWithNoActionIsRefused(t *testing.T) {
	s := openMem(t)
	err := s.AddACLRow(core.ACLRow{Principal: "github:bob", Scope: "main/**"})
	if err == nil {
		t.Fatal("a grant with no action was stored; it grants nothing and reads " +
			"in the table like one that does")
	}
	if !strings.Contains(err.Error(), "names nothing") {
		t.Errorf("refusal was %q; it must say the row names nothing", err)
	}
}
