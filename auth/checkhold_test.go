package auth

import (
	"testing"

	"github.com/kronael/arizuko/core"
)

func TestCheckHold_NoRowsRunInline(t *testing.T) {
	s := openMem(t)
	if CheckHold(s, Caller{Principal: "folder:atlas"}, "mcp:send", "atlas", nil) != "" {
		t.Fatal("no hold row should run inline")
	}
}

func TestCheckHold_BareRuleAlwaysHolds(t *testing.T) {
	s := openMem(t)
	addRow(t, s, "folder:atlas", HoldPrefix+"mcp:delete", "atlas", "allow")
	if CheckHold(s, Caller{Principal: "folder:atlas"}, "mcp:delete", "atlas", nil) == "" {
		t.Fatal("bare hold rule should hold")
	}
}

func TestCheckHold_OtherToolUnaffected(t *testing.T) {
	s := openMem(t)
	addRow(t, s, "folder:atlas", HoldPrefix+"mcp:delete", "atlas", "allow")
	if CheckHold(s, Caller{Principal: "folder:atlas"}, "mcp:send", "atlas", nil) != "" {
		t.Fatal("a hold on delete must not hold send")
	}
}

// TestCheckHold_OperatorStarDoesNotHoldEverything is spec 5/19 Hazard 2. An
// operator's (*, **) row covers every action under Authorize's lattice, so
// routing the hold question through Authorize would suspend every tool call the
// operator makes. CheckHold matches the action exactly instead.
func TestCheckHold_OperatorStarDoesNotHoldEverything(t *testing.T) {
	s := openMem(t)
	addRow(t, s, "role:operator", "*", "**", "allow")
	if err := s.AddMembership("google:alice", "role:operator", "test"); err != nil {
		t.Fatalf("AddMembership: %v", err)
	}
	caller := Caller{Principal: "google:alice"}
	if !Authorize(s, caller, "mcp:delete", "atlas", nil) {
		t.Fatal("precondition: operator should be authorized for the tool")
	}
	if CheckHold(s, caller, "mcp:delete", "atlas", nil) != "" {
		t.Fatal("operator (*, **) must NOT hold every tool")
	}
}

// TestCheckHold_PlainAllowRowIsNotAHold is Hazard 1 from the other side: a
// grant row for the tool must never read as a hold, or every granted tool would
// suspend.
func TestCheckHold_PlainAllowRowIsNotAHold(t *testing.T) {
	s := openMem(t)
	addRow(t, s, "folder:atlas", "mcp:delete", "atlas", "allow")
	if CheckHold(s, Caller{Principal: "folder:atlas"}, "mcp:delete", "atlas", nil) != "" {
		t.Fatal("a plain grant row must not be read as a hold")
	}
}

func TestCheckHold_ParamsMakeItConditional(t *testing.T) {
	s := openMem(t)
	addRowFull(t, s, core.ACLRow{
		Principal: "folder:atlas", Action: HoldPrefix + "mcp:network_allow",
		Scope: "atlas", Effect: "allow", Params: "host=*.prod.example.com",
	})
	caller := Caller{Principal: "folder:atlas"}
	if CheckHold(s, caller, "mcp:network_allow", "atlas", map[string]string{"host": "db.prod.example.com"}) == "" {
		t.Fatal("matching param should hold")
	}
	if CheckHold(s, caller, "mcp:network_allow", "atlas", map[string]string{"host": "docs.example.com"}) != "" {
		t.Fatal("non-matching param should run inline")
	}
}

func TestCheckHold_ScopeContains(t *testing.T) {
	s := openMem(t)
	addRow(t, s, "folder:atlas", HoldPrefix+"mcp:delete", "atlas/**", "allow")
	caller := Caller{Principal: "folder:atlas"}
	if CheckHold(s, caller, "mcp:delete", "atlas/eng", nil) == "" {
		t.Fatal("subtree scope should cover the child folder")
	}
	if CheckHold(s, caller, "mcp:delete", "other", nil) != "" {
		t.Fatal("scope must not reach outside the subtree")
	}
}

func TestCheckHold_WildcardToolRule(t *testing.T) {
	s := openMem(t)
	addRow(t, s, "folder:atlas", HoldPrefix+"*", "atlas", "allow")
	if CheckHold(s, Caller{Principal: "folder:atlas"}, "mcp:anything", "atlas", nil) == "" {
		t.Fatal("hold:mcp:* should hold every tool for that folder")
	}
}

func TestCheckHold_NilStoreAndEmptyInputs(t *testing.T) {
	s := openMem(t)
	if CheckHold(nil, Caller{Principal: "folder:atlas"}, "mcp:x", "atlas", nil) != "" {
		t.Fatal("nil store should not hold")
	}
	if CheckHold(s, Caller{}, "mcp:x", "atlas", nil) != "" {
		t.Fatal("empty principal should not hold")
	}
	if CheckHold(s, Caller{Principal: "folder:atlas"}, "", "atlas", nil) != "" {
		t.Fatal("empty tool should not hold")
	}
}

// Spec 6/26 § Decision 5. A skip-approval row (`effect='deny'` on a `hold:`
// row) is the ONLY way an "always allow this shape" answer can be expressed:
// it denies the hold, not the tool. Without it CheckHold had no exception at
// all, and the obvious alternative — an allow row on plain `mcp:<tool>` — is
// Hazard 1, granting the tool while the hold still fires.
func TestCheckHold_SkipApprovalRowBeatsMatchingHold(t *testing.T) {
	s := openMem(t)
	addRow(t, s, "folder:atlas", HoldPrefix+"mcp:delete", "atlas", "allow")
	addRowFull(t, s, core.ACLRow{
		Principal: "folder:atlas", Action: HoldPrefix + "mcp:delete",
		Scope: "atlas", Effect: EffectSkipApproval, Params: "target=staging",
	})
	c := Caller{Principal: "folder:atlas"}

	if CheckHold(s, c, "mcp:delete", "atlas", map[string]string{"target": "staging"}) != "" {
		t.Error("the skipped shape must run inline")
	}
	if CheckHold(s, c, "mcp:delete", "atlas", map[string]string{"target": "prod"}) == "" {
		t.Error("every other shape must still be held")
	}
}

// Deny-wins is order-independent and absolute: a broad hold:mcp:* cannot
// resurrect a shape an exception released. Revocation is removing the
// skip-approval row, never adding a narrower hold on top.
func TestCheckHold_BroadHoldDoesNotResurrectSkippedShape(t *testing.T) {
	s := openMem(t)
	addRowFull(t, s, core.ACLRow{
		Principal: "folder:atlas", Action: HoldPrefix + "mcp:delete",
		Scope: "atlas", Effect: EffectSkipApproval, Params: "target=staging",
	})
	addRow(t, s, "folder:atlas", HoldPrefix+"*", "atlas", "allow")
	c := Caller{Principal: "folder:atlas"}

	if CheckHold(s, c, "mcp:delete", "atlas", map[string]string{"target": "staging"}) != "" {
		t.Error("a later broad hold must not override the exception")
	}
	if CheckHold(s, c, "mcp:send", "atlas", nil) == "" {
		t.Error("hold:mcp:* must still hold a tool the exception does not name")
	}
}

// The exception lives in the `hold:` namespace, so it must stay invisible to
// the allow/deny evaluator: a skip-approval row must never read as a DENIAL of
// the tool itself, or "always" would silently become "never".
func TestCheckHold_SkipApprovalDoesNotDenyTheTool(t *testing.T) {
	s := openMem(t)
	addRow(t, s, "folder:atlas", "mcp:delete", "atlas", "allow")
	addRowFull(t, s, core.ACLRow{
		Principal: "folder:atlas", Action: HoldPrefix + "mcp:delete",
		Scope: "atlas", Effect: EffectSkipApproval,
	})
	if !Authorize(s, Caller{Principal: "folder:atlas"}, "mcp:delete", "atlas", nil) {
		t.Fatal("a skip-approval row must not deny the tool it releases")
	}
}

// Production passes the RAW MCP tool name — ipc hands CheckHold
// `p.Params.Name` ("reply", "add_acl"), and routd prefixes it once
// (`HoldPrefix + tool`). The cases above use "mcp:delete", which is
// self-consistent but builds `hold:mcp:mcp:delete` and so would not notice a
// prefix bug. This one uses the shape production actually sends.
func TestCheckHold_RawToolNameIsWhatProductionSends(t *testing.T) {
	s := openMem(t)
	addRow(t, s, "folder:atlas", HoldPrefix+"reply", "atlas", "allow")
	c := Caller{Principal: "folder:atlas"}
	if CheckHold(s, c, "reply", "atlas", nil) == "" {
		t.Fatal("a hold on the raw tool name must hold it")
	}
	addRowFull(t, s, core.ACLRow{
		Principal: "folder:atlas", Action: HoldPrefix + "reply",
		Scope: "atlas", Effect: EffectSkipApproval,
	})
	if CheckHold(s, c, "reply", "atlas", nil) != "" {
		t.Fatal("the skip-approval row must release the raw tool name too")
	}
}

// Deny-wins must not depend on row order. The acl read has no ORDER BY, so a
// test that inserts skip-then-hold proves nothing a first-match implementation
// would fail; this inserts BOTH orders in two stores and demands one answer.
func TestCheckHold_SkipWinsInEitherInsertOrder(t *testing.T) {
	skip := core.ACLRow{
		Principal: "folder:atlas", Action: HoldPrefix + "deploy",
		Scope: "atlas", Effect: EffectSkipApproval,
	}
	hold := core.ACLRow{
		Principal: "folder:atlas", Action: HoldPrefix + "deploy",
		Scope: "atlas", Effect: "allow",
	}
	for _, tc := range []struct {
		name          string
		first, second core.ACLRow
	}{
		{"hold first", hold, skip},
		{"skip first", skip, hold},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := openMem(t)
			addRowFull(t, s, tc.first)
			addRowFull(t, s, tc.second)
			if CheckHold(s, Caller{Principal: "folder:atlas"}, "deploy", "atlas", nil) != "" {
				t.Error("a skip-approval row must win regardless of insert order")
			}
		})
	}
}

// The skip may be INHERITED: expandPrincipals follows acl_membership, so a role
// the folder belongs to can carry the exception. Specificity does not exist —
// an inherited skip beats a directly-held hold, by design.
func TestCheckHold_InheritedSkipBeatsDirectHold(t *testing.T) {
	s := openMem(t)
	addRow(t, s, "folder:atlas", HoldPrefix+"deploy", "atlas", "allow")
	addRowFull(t, s, core.ACLRow{
		Principal: "role:release", Action: HoldPrefix + "deploy",
		Scope: "atlas", Effect: EffectSkipApproval,
	})
	if err := s.AddMembership("folder:atlas", "role:release", "test"); err != nil {
		t.Fatalf("AddMembership: %v", err)
	}
	if CheckHold(s, Caller{Principal: "folder:atlas"}, "deploy", "atlas", nil) != "" {
		t.Error("an inherited skip-approval row must release the hold")
	}
}

// TestEffectAllowed_AsyncOnlyOnAHoldRule — `async` on a plain mcp: row would
// read as an ordinary ALLOW (spec 5/19 Hazard 1) and GRANT the tool it looks
// like it is gating. Operator surfaces refuse it there.
func TestEffectAllowed_AsyncOnlyOnAHoldRule(t *testing.T) {
	for _, c := range []struct {
		action, effect string
		want           bool
	}{
		{HoldPrefix + "mcp:delete", EffectHoldAsync, true},
		{HoldPrefix + "mcp:delete", "allow", true},
		{HoldPrefix + "mcp:delete", EffectSkipApproval, true},
		{"mcp:delete", EffectHoldAsync, false},
		{"mcp:delete", "allow", true},
		{HoldPrefix + "mcp:delete", "maybe", false},
	} {
		if got := EffectAllowed(c.action, c.effect); got != c.want {
			t.Errorf("EffectAllowed(%q, %q) = %v, want %v", c.action, c.effect, got, c.want)
		}
	}
}

// TestCheckHold_AsyncRowSaysAsync — the non-blocking variant has to survive the
// read, or `block by default` would silently become `block always`.
func TestCheckHold_AsyncRowSaysAsync(t *testing.T) {
	s := openMem(t)
	addRow(t, s, "folder:atlas", HoldPrefix+"mcp:delete", "atlas", EffectHoldAsync)
	if got := CheckHold(s, Caller{Principal: "folder:atlas"}, "mcp:delete", "atlas", nil); got != EffectHoldAsync {
		t.Fatalf("CheckHold = %q, want %q", got, EffectHoldAsync)
	}
}

// TestCheckHold_BlockingWinsOverAsync — two rules match and they disagree. The
// safer reading of a disagreement is the one that waits.
func TestCheckHold_BlockingWinsOverAsync(t *testing.T) {
	s := openMem(t)
	addRow(t, s, "folder:atlas", HoldPrefix+"mcp:delete", "atlas", EffectHoldAsync)
	addRow(t, s, "folder:atlas", HoldPrefix+"*", "atlas", "allow")
	if got := CheckHold(s, Caller{Principal: "folder:atlas"}, "mcp:delete", "atlas", nil); got != EffectHoldBlock {
		t.Fatalf("CheckHold = %q, want the blocking effect %q", got, EffectHoldBlock)
	}
}
