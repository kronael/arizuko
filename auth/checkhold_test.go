package auth_test

import (
	"strings"
	"testing"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/store"
)

func TestCheckHold_NoRowsRunInline(t *testing.T) {
	s := openMem(t)
	if auth.CheckHold(s, auth.Caller{Principal: "folder:atlas"}, "mcp:send", "atlas", nil) {
		t.Fatal("no hold row should run inline")
	}
}

func TestCheckHold_BareRuleAlwaysHolds(t *testing.T) {
	s := openMem(t)
	addRow(t, s, "folder:atlas", auth.HoldPrefix+"mcp:delete", "atlas", "allow")
	if !auth.CheckHold(s, auth.Caller{Principal: "folder:atlas"}, "mcp:delete", "atlas", nil) {
		t.Fatal("bare hold rule should hold")
	}
}

func TestCheckHold_OtherToolUnaffected(t *testing.T) {
	s := openMem(t)
	addRow(t, s, "folder:atlas", auth.HoldPrefix+"mcp:delete", "atlas", "allow")
	if auth.CheckHold(s, auth.Caller{Principal: "folder:atlas"}, "mcp:send", "atlas", nil) {
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
	caller := auth.Caller{Principal: "google:alice"}
	if !auth.Authorize(s, caller, "mcp:delete", "atlas", nil) {
		t.Fatal("precondition: operator should be authorized for the tool")
	}
	if auth.CheckHold(s, caller, "mcp:delete", "atlas", nil) {
		t.Fatal("operator (*, **) must NOT hold every tool")
	}
}

// TestCheckHold_PlainAllowRowIsNotAHold is Hazard 1 from the other side: a
// grant row for the tool must never read as a hold, or every granted tool would
// suspend.
func TestCheckHold_PlainAllowRowIsNotAHold(t *testing.T) {
	s := openMem(t)
	addRow(t, s, "folder:atlas", "mcp:delete", "atlas", "allow")
	if auth.CheckHold(s, auth.Caller{Principal: "folder:atlas"}, "mcp:delete", "atlas", nil) {
		t.Fatal("a plain grant row must not be read as a hold")
	}
}

func TestCheckHold_ParamsMakeItConditional(t *testing.T) {
	s := openMem(t)
	addRowFull(t, s, core.ACLRow{
		Principal: "folder:atlas", Action: auth.HoldPrefix + "mcp:network_allow",
		Scope: "atlas", Effect: "allow", Params: "host=*.prod.example.com",
	})
	caller := auth.Caller{Principal: "folder:atlas"}
	if !auth.CheckHold(s, caller, "mcp:network_allow", "atlas", map[string]string{"host": "db.prod.example.com"}) {
		t.Fatal("matching param should hold")
	}
	if auth.CheckHold(s, caller, "mcp:network_allow", "atlas", map[string]string{"host": "docs.example.com"}) {
		t.Fatal("non-matching param should run inline")
	}
}

func TestCheckHold_ScopeContains(t *testing.T) {
	s := openMem(t)
	addRow(t, s, "folder:atlas", auth.HoldPrefix+"mcp:delete", "atlas/**", "allow")
	caller := auth.Caller{Principal: "folder:atlas"}
	if !auth.CheckHold(s, caller, "mcp:delete", "atlas/eng", nil) {
		t.Fatal("subtree scope should cover the child folder")
	}
	if auth.CheckHold(s, caller, "mcp:delete", "other", nil) {
		t.Fatal("scope must not reach outside the subtree")
	}
}

func TestCheckHold_WildcardToolRule(t *testing.T) {
	s := openMem(t)
	addRow(t, s, "folder:atlas", auth.HoldPrefix+"*", "atlas", "allow")
	if !auth.CheckHold(s, auth.Caller{Principal: "folder:atlas"}, "mcp:anything", "atlas", nil) {
		t.Fatal("hold:mcp:* should hold every tool for that folder")
	}
}

func TestCheckHold_NilStoreAndEmptyInputs(t *testing.T) {
	s := openMem(t)
	if auth.CheckHold(nil, auth.Caller{Principal: "folder:atlas"}, "mcp:x", "atlas", nil) {
		t.Fatal("nil store should not hold")
	}
	if auth.CheckHold(s, auth.Caller{}, "mcp:x", "atlas", nil) {
		t.Fatal("empty principal should not hold")
	}
	if auth.CheckHold(s, auth.Caller{Principal: "folder:atlas"}, "", "atlas", nil) {
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
	addRow(t, s, "folder:atlas", auth.HoldPrefix+"mcp:delete", "atlas", "allow")
	addRowFull(t, s, core.ACLRow{
		Principal: "folder:atlas", Action: auth.HoldPrefix + "mcp:delete",
		Scope: "atlas", Effect: auth.EffectSkipApproval, Params: "target=staging",
	})
	c := auth.Caller{Principal: "folder:atlas"}

	if auth.CheckHold(s, c, "mcp:delete", "atlas", map[string]string{"target": "staging"}) {
		t.Error("the skipped shape must run inline")
	}
	if !auth.CheckHold(s, c, "mcp:delete", "atlas", map[string]string{"target": "prod"}) {
		t.Error("every other shape must still be held")
	}
}

// Deny-wins is order-independent and absolute: a broad hold:mcp:* cannot
// resurrect a shape an exception released. Revocation is removing the
// skip-approval row, never adding a narrower hold on top.
func TestCheckHold_BroadHoldDoesNotResurrectSkippedShape(t *testing.T) {
	s := openMem(t)
	addRowFull(t, s, core.ACLRow{
		Principal: "folder:atlas", Action: auth.HoldPrefix + "mcp:delete",
		Scope: "atlas", Effect: auth.EffectSkipApproval, Params: "target=staging",
	})
	addRow(t, s, "folder:atlas", auth.HoldPrefix+"*", "atlas", "allow")
	c := auth.Caller{Principal: "folder:atlas"}

	if auth.CheckHold(s, c, "mcp:delete", "atlas", map[string]string{"target": "staging"}) {
		t.Error("a later broad hold must not override the exception")
	}
	if !auth.CheckHold(s, c, "mcp:send", "atlas", nil) {
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
		Principal: "folder:atlas", Action: auth.HoldPrefix + "mcp:delete",
		Scope: "atlas", Effect: auth.EffectSkipApproval,
	})
	if !auth.Authorize(s, auth.Caller{Principal: "folder:atlas"}, "mcp:delete", "atlas", nil) {
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
	addRow(t, s, "folder:atlas", auth.HoldPrefix+"reply", "atlas", "allow")
	c := auth.Caller{Principal: "folder:atlas"}
	if !auth.CheckHold(s, c, "reply", "atlas", nil) {
		t.Fatal("a hold on the raw tool name must hold it")
	}
	addRowFull(t, s, core.ACLRow{
		Principal: "folder:atlas", Action: auth.HoldPrefix + "reply",
		Scope: "atlas", Effect: auth.EffectSkipApproval,
	})
	if auth.CheckHold(s, c, "reply", "atlas", nil) {
		t.Fatal("the skip-approval row must release the raw tool name too")
	}
}

// Deny-wins must not depend on row order. The acl read has no ORDER BY, so a
// test that inserts skip-then-hold proves nothing a first-match implementation
// would fail; this inserts BOTH orders in two stores and demands one answer.
func TestCheckHold_SkipWinsInEitherInsertOrder(t *testing.T) {
	skip := core.ACLRow{
		Principal: "folder:atlas", Action: auth.HoldPrefix + "deploy",
		Scope: "atlas", Effect: auth.EffectSkipApproval,
	}
	hold := core.ACLRow{
		Principal: "folder:atlas", Action: auth.HoldPrefix + "deploy",
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
			if auth.CheckHold(s, auth.Caller{Principal: "folder:atlas"}, "deploy", "atlas", nil) {
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
	addRow(t, s, "folder:atlas", auth.HoldPrefix+"deploy", "atlas", "allow")
	addRowFull(t, s, core.ACLRow{
		Principal: "role:release", Action: auth.HoldPrefix + "deploy",
		Scope: "atlas", Effect: auth.EffectSkipApproval,
	})
	if err := s.AddMembership("folder:atlas", "role:release", "test"); err != nil {
		t.Fatalf("AddMembership: %v", err)
	}
	if auth.CheckHold(s, auth.Caller{Principal: "folder:atlas"}, "deploy", "atlas", nil) {
		t.Error("an inherited skip-approval row must release the hold")
	}
}

// TestCheckHold_LegacyNonDenyEffectStillHolds — a hold row is a hold whatever
// its allow is spelled. The writer now refuses every spelling but `allow` and
// `deny`, so this is about rows a live database already carries: reading one as
// "not a hold" would silently drop the gate it was written to apply.
func TestCheckHold_LegacyNonDenyEffectStillHolds(t *testing.T) {
	s := openMem(t)
	addLegacyEffectRow(t, s, "folder:atlas", auth.HoldPrefix+"mcp:delete", "atlas", "async")
	if !auth.CheckHold(s, auth.Caller{Principal: "folder:atlas"}, "mcp:delete", "atlas", nil) {
		t.Fatal("a hold row stopped holding because of how its allow is spelled")
	}
}

// TestCheckHold_TwoMatchingRulesStillHold — two rules match. There is one answer
// a hold rule can give, so they cannot disagree and the call waits.
func TestCheckHold_TwoMatchingRulesStillHold(t *testing.T) {
	s := openMem(t)
	addRow(t, s, "folder:atlas", auth.HoldPrefix+"mcp:delete", "atlas", auth.EffectHoldBlock)
	addRow(t, s, "folder:atlas", auth.HoldPrefix+"*", "atlas", auth.EffectHoldBlock)
	if !auth.CheckHold(s, auth.Caller{Principal: "folder:atlas"}, "mcp:delete", "atlas", nil) {
		t.Fatal("two matching hold rules must still hold")
	}
}

// addLegacyEffectRow writes an acl item whose effect no writer would accept
// today, straight at the coordinate. store.aclPut refuses the spelling now
// (BUGS L272), and a database written before it did is exactly what this has to
// stand in for.
func addLegacyEffectRow(t *testing.T, s *store.Store, principal, action, scope, effect string) {
	t.Helper()
	sort := strings.Join([]string{principal, action, scope, "", "", effect}, store.KeySep)
	if err := s.WriteEntities(t.Context(), nil, objstore.Write{
		Op: objstore.Put, Entity: store.ACL,
		Key:   objstore.Key{Partition: store.OwnerRoutd, Sort: sort},
		Attrs: map[string]string{"grant_option": "0", "granted_by": "legacy"},
	}); err != nil {
		t.Fatalf("write the legacy row: %v", err)
	}
}

// TestCheckHold_PredicateIsInertOnTheLiveCaller pins the `hold:` half of BUGS
// L134. The acl write refuses a predicate on a `shell:env:` row because that
// resolver carries no claims, and it EXEMPTS `hold:` on the stated ground that
// "CheckHold passes the caller's claims, so a hold: predicate is evaluated
// rather than dead" (store/acl_test.go). CheckHold does pass them. Nothing
// fills them: routd asks every hold question as auth.Caller{Principal: sub}
// (routd/sibling_db.go CheckHold, from routd/pending_actions.go holdGate), and
// the two sites that DO populate auth.Caller.Claims (resreg.authCaller, fed by
// routd/server.go) reach Authorize and never this one. So predicateMatches
// answers false for any non-empty predicate and the rule matches nothing.
//
// The direction is the OPPOSITE of the shell:env: trap, and worse. There a
// never-matching row withholds a key; here it withholds the GATE, so the call
// the operator wrote the rule to suspend runs unheld and nothing says so.
func TestCheckHold_PredicateIsInertOnTheLiveCaller(t *testing.T) {
	s := openMem(t)
	addRowFull(t, s, core.ACLRow{
		Principal: "folder:atlas", Action: auth.HoldPrefix + "mcp:delete",
		Scope: "atlas", Effect: auth.EffectHoldBlock, Predicate: "platform=telegram",
	})
	// Exactly the caller routd builds: a principal and nothing else.
	live := auth.Caller{Principal: "folder:atlas"}
	if auth.CheckHold(s, live, "mcp:delete", "atlas", nil) {
		t.Fatal("a predicated hold rule matched a claimless caller — the trap is closed and this test is stale")
	}

	// The predicate is what kills it, not the action or the scope: the same row
	// without one holds for the same caller.
	bare := openMem(t)
	addRow(t, bare, "folder:atlas", auth.HoldPrefix+"mcp:delete", "atlas", auth.EffectHoldBlock)
	if !auth.CheckHold(bare, live, "mcp:delete", "atlas", nil) {
		t.Fatal("the bare hold rule stopped holding, so this case no longer isolates the predicate")
	}

	// And the EVALUATOR is not the gap. Hand the caller the claim the operator
	// meant and the same row holds, so what is missing is a caller carrying the
	// trigger's platform — the open question L134 records, not a defect here.
	withClaim := auth.Caller{
		Principal: "folder:atlas",
		Claims:    map[string]string{"platform": "telegram"},
	}
	if !auth.CheckHold(s, withClaim, "mcp:delete", "atlas", nil) {
		t.Fatal("the predicate did not match even with the claim present; the gap would not be the caller")
	}
}
