package routd

// Spec 6/26 gap 2.3: a SHAPED `never` cannot work, so no surface may offer one.
//
// The hold path passes arguments — holdGate hands CheckHold stringArgs(args) —
// but the authz path does not: ipc's granted() calls authorizeCall(name, nil),
// and auth.paramsMatch requires every named key to be PRESENT, so a deny row
// shaped `mcp:<tool>(k=v)` matches nothing and denies nothing. A shaped `never`
// would look recorded and refuse no call.
//
// The restriction is v1's answer; this test is its REASON, written down so
// removing the restriction requires removing the reason. Carrying arguments
// into the authz path is its own change, with every tool in its blast radius.

import (
	"testing"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/store"
)

// A shaped deny is inert against the nil params the authz path passes, while
// the tool-wide deny on the same tool bites. That asymmetry is exactly why v1
// offers a shaped `always` and no shaped `never`.
func TestShapedDenyIsInertUnderNilParams(t *testing.T) {
	d := memDB(t)
	s := store.NewOver(d.Obj())
	if err := addACLRow(d, core.ACLRow{
		Principal: "folder:atlas", Action: "mcp:delete", Scope: "atlas", Effect: "allow",
	}); err != nil {
		t.Fatal(err)
	}
	if err := addACLRow(d, core.ACLRow{
		Principal: "folder:atlas", Action: "mcp:delete", Scope: "atlas",
		Effect: "deny", Params: "target=prod",
	}); err != nil {
		t.Fatal(err)
	}
	caller := auth.Caller{Principal: "folder:atlas"}

	// What ipc's granted() actually asks — nil params. The shaped deny cannot
	// match it, so the call is ALLOWED despite a deny row naming it.
	if !auth.Authorize(s, caller, "mcp:delete", "atlas", nil) {
		t.Fatal("precondition: with nil params the shaped deny must not match")
	}
	// The same row DOES deny once the arguments travel — so the row is not
	// wrong, the path that never carries arguments is.
	if auth.Authorize(s, caller, "mcp:delete", "atlas", map[string]string{"target": "prod"}) {
		t.Fatal("with the arguments passed, the shaped deny must win")
	}

	// A TOOL-WIDE deny bites either way. That is the only `never` v1 could
	// honestly write — and it is not offered for a different reason: revocation
	// has no provenance (§ "Revocation needs provenance").
	if err := addACLRow(d, core.ACLRow{
		Principal: "folder:atlas", Action: "mcp:send", Scope: "atlas", Effect: "allow",
	}); err != nil {
		t.Fatal(err)
	}
	if err := addACLRow(d, core.ACLRow{
		Principal: "folder:atlas", Action: "mcp:send", Scope: "atlas", Effect: "deny",
	}); err != nil {
		t.Fatal(err)
	}
	if auth.Authorize(s, caller, "mcp:send", "atlas", nil) {
		t.Fatal("a tool-wide deny must refuse the call the authz path actually makes")
	}
}

// The hold path is the other half of the asymmetry: it DOES carry arguments, so
// a shaped rule there works. That is why `always` — which writes only a `hold:`
// row — may be shaped while `never` may not.
func TestHoldPathCarriesArgumentsWhereAuthzDoesNot(t *testing.T) {
	d := memDB(t)
	if err := addACLRow(d, core.ACLRow{
		Principal: "folder:atlas", Action: auth.HoldPrefix + "delete",
		Scope: "atlas", Effect: "allow", Params: "target=prod",
	}); err != nil {
		t.Fatal(err)
	}
	srv := &Server{db: d}
	gate := srv.holdGate(turnMCP{folder: "atlas"})
	if _, held := gate("delete", map[string]any{"target": "prod"}); !held {
		t.Error("the hold path must match a shaped rule against the call's arguments")
	}
	if _, held := gate("delete", map[string]any{"target": "staging"}); held {
		t.Error("a shape the rule does not name must run inline")
	}
}
