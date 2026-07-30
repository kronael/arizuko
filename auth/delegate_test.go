package auth

import (
	"testing"

	"github.com/kronael/arizuko/core"
)

// spec 4/R: Delegate enforces subset-of-held-WITH-GRANT-OPTION. These cases
// stand in for tier-masking: authority narrows down a delegation chain by data,
// not by folder depth.
func TestDelegate(t *testing.T) {
	row := func(p, a, sc string, opt bool) core.ACLRow {
		return core.ACLRow{Principal: p, Action: a, Scope: sc, Effect: "allow", GrantOption: opt}
	}

	t.Run("operator delegates anything", func(t *testing.T) {
		s := openMem(t)
		// role:operator (*, **) WITH GRANT OPTION is seeded by migration 0075; a
		// member inherits it transitively.
		if err := s.AddMembership("google:alice", "role:operator", "test"); err != nil {
			t.Fatal(err)
		}
		if err := Delegate(s, "google:alice", []core.ACLRow{
			row("folder:acme/eng", "mcp:send", "acme/eng", false),
			row("folder:x", "admin", "x/**", true),
		}); err != nil {
			t.Fatalf("operator should delegate anything: %v", err)
		}
	})

	t.Run("owner delegates within its subtree only", func(t *testing.T) {
		s := openMem(t)
		addRowFull(t, s, row("folder:acme", "admin", "acme/**", true))
		// admin covers mcp:send; acme/** covers acme/eng.
		if err := Delegate(s, "folder:acme", []core.ACLRow{
			row("folder:acme/eng", "mcp:send", "acme/eng", false),
		}); err != nil {
			t.Fatalf("in-subtree delegation should pass: %v", err)
		}
		// Outside the held scope → rejected.
		if err := Delegate(s, "folder:acme", []core.ACLRow{
			row("folder:other", "mcp:send", "other/x", false),
		}); err == nil {
			t.Fatal("out-of-subtree delegation must fail")
		}
	})

	t.Run("no grant option → cannot delegate", func(t *testing.T) {
		s := openMem(t)
		addRowFull(t, s, row("folder:acme", "admin", "acme/**", false)) // held WITHOUT option
		if err := Delegate(s, "folder:acme", []core.ACLRow{
			row("folder:acme/eng", "mcp:send", "acme/eng", false),
		}); err == nil {
			t.Fatal("delegation without grant option must fail")
		}
	})

	t.Run("cannot broaden the action", func(t *testing.T) {
		s := openMem(t)
		addRowFull(t, s, row("folder:acme", "mcp:send", "acme/**", true)) // only mcp:send
		if err := Delegate(s, "folder:acme", []core.ACLRow{
			row("folder:acme/eng", "admin", "acme/eng", false), // admin ⊃ mcp:send
		}); err == nil {
			t.Fatal("delegating a broader action than held must fail")
		}
	})
}
