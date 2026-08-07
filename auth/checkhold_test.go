package auth

import (
	"testing"

	"github.com/kronael/arizuko/core"
)

func TestCheckHold_NoRowsRunInline(t *testing.T) {
	s := openMem(t)
	if CheckHold(s, Caller{Principal: "folder:atlas"}, "mcp:send", "atlas", nil) {
		t.Fatal("no hold row should run inline")
	}
}

func TestCheckHold_BareRuleAlwaysHolds(t *testing.T) {
	s := openMem(t)
	addRow(t, s, "folder:atlas", HoldPrefix+"mcp:delete", "atlas", "allow")
	if !CheckHold(s, Caller{Principal: "folder:atlas"}, "mcp:delete", "atlas", nil) {
		t.Fatal("bare hold rule should hold")
	}
}

func TestCheckHold_OtherToolUnaffected(t *testing.T) {
	s := openMem(t)
	addRow(t, s, "folder:atlas", HoldPrefix+"mcp:delete", "atlas", "allow")
	if CheckHold(s, Caller{Principal: "folder:atlas"}, "mcp:send", "atlas", nil) {
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
	if CheckHold(s, caller, "mcp:delete", "atlas", nil) {
		t.Fatal("operator (*, **) must NOT hold every tool")
	}
}

// TestCheckHold_PlainAllowRowIsNotAHold is Hazard 1 from the other side: a
// grant row for the tool must never read as a hold, or every granted tool would
// suspend.
func TestCheckHold_PlainAllowRowIsNotAHold(t *testing.T) {
	s := openMem(t)
	addRow(t, s, "folder:atlas", "mcp:delete", "atlas", "allow")
	if CheckHold(s, Caller{Principal: "folder:atlas"}, "mcp:delete", "atlas", nil) {
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
	if !CheckHold(s, caller, "mcp:network_allow", "atlas", map[string]string{"host": "db.prod.example.com"}) {
		t.Fatal("matching param should hold")
	}
	if CheckHold(s, caller, "mcp:network_allow", "atlas", map[string]string{"host": "docs.example.com"}) {
		t.Fatal("non-matching param should run inline")
	}
}

func TestCheckHold_ScopeContains(t *testing.T) {
	s := openMem(t)
	addRow(t, s, "folder:atlas", HoldPrefix+"mcp:delete", "atlas/**", "allow")
	caller := Caller{Principal: "folder:atlas"}
	if !CheckHold(s, caller, "mcp:delete", "atlas/eng", nil) {
		t.Fatal("subtree scope should cover the child folder")
	}
	if CheckHold(s, caller, "mcp:delete", "other", nil) {
		t.Fatal("scope must not reach outside the subtree")
	}
}

func TestCheckHold_WildcardToolRule(t *testing.T) {
	s := openMem(t)
	addRow(t, s, "folder:atlas", HoldPrefix+"*", "atlas", "allow")
	if !CheckHold(s, Caller{Principal: "folder:atlas"}, "mcp:anything", "atlas", nil) {
		t.Fatal("hold:mcp:* should hold every tool for that folder")
	}
}

func TestCheckHold_NilStoreAndEmptyInputs(t *testing.T) {
	s := openMem(t)
	if CheckHold(nil, Caller{Principal: "folder:atlas"}, "mcp:x", "atlas", nil) {
		t.Fatal("nil store should not hold")
	}
	if CheckHold(s, Caller{}, "mcp:x", "atlas", nil) {
		t.Fatal("empty principal should not hold")
	}
	if CheckHold(s, Caller{Principal: "folder:atlas"}, "", "atlas", nil) {
		t.Fatal("empty tool should not hold")
	}
}
