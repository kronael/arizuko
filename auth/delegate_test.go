package auth

import (
	"testing"

	"github.com/kronael/arizuko/core"
)

// spec 5/33: Delegate enforces subset-of-held-WITH-GRANT-OPTION. These cases
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

	t.Run("cannot broaden the scope glob (F3)", func(t *testing.T) {
		s := openMem(t)
		addRowFull(t, s, row("folder:acme", "admin", "acme/*", true)) // single-segment glob
		// acme/* must NOT cover acme/** (that spans acme/eng/sre the granter lacks).
		if err := Delegate(s, "folder:acme", []core.ACLRow{
			row("folder:x", "admin", "acme/**", false),
		}); err == nil {
			t.Fatal("acme/* must not delegate acme/**")
		}
		// But acme/** DOES cover a deeper concrete path and deeper glob.
		addRowFull(t, s, row("folder:acme", "admin", "acme/**", true))
		if err := Delegate(s, "folder:acme", []core.ACLRow{
			row("folder:x", "admin", "acme/eng/sre", false),
			row("folder:y", "admin", "acme/eng/**", false),
		}); err != nil {
			t.Fatalf("acme/** should cover deeper paths: %v", err)
		}
	})

	t.Run("cannot delegate past own deny, nor to wildcard principal (F4)", func(t *testing.T) {
		s := openMem(t)
		addRowFull(t, s, row("folder:acme", "admin", "acme/**", true))
		addRowFull(t, s, core.ACLRow{Principal: "folder:acme", Action: "admin", Scope: "acme/secret", Effect: "deny"})
		// Held admin+GO on acme/** but a deny on acme/secret → can't delegate there.
		if err := Delegate(s, "folder:acme", []core.ACLRow{
			row("folder:x", "admin", "acme/secret", false),
		}); err == nil {
			t.Fatal("must not delegate past own deny")
		}
		// Wildcard principal is an escalation → refused.
		if err := Delegate(s, "folder:acme", []core.ACLRow{
			row("*", "admin", "acme/eng", false),
		}); err == nil {
			t.Fatal("must not delegate to a wildcard principal")
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
