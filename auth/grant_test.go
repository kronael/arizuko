package auth_test

import (
	"testing"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/store"
)

// spec 5/33-paths-roles: Delegate enforces subset-of-held-WITH-GRANT-OPTION. These cases
// stand in for tier-masking: authority narrows down a delegation chain by data,
// not by folder depth.
func TestDelegate(t *testing.T) {
	row := func(p, a, sc string, opt bool) core.ACLRow {
		return core.ACLRow{Principal: p, Action: a, Scope: sc, Effect: "allow", GrantOption: opt}
	}

	t.Run("operator delegates anything", func(t *testing.T) {
		s := openMem(t)
		// role:operator (*, **) WITH GRANT OPTION is seeded by
		// routd/migrations/0022; a member inherits it transitively.
		if err := s.AddMembership("google:alice", "role:operator", "test"); err != nil {
			t.Fatal(err)
		}
		if err := delegateForTest(s, "google:alice", []core.ACLRow{
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
		if err := delegateForTest(s, "folder:acme", []core.ACLRow{
			row("folder:acme/eng", "mcp:send", "acme/eng", false),
		}); err != nil {
			t.Fatalf("in-subtree delegation should pass: %v", err)
		}
		// Outside the held scope → rejected.
		if err := delegateForTest(s, "folder:acme", []core.ACLRow{
			row("folder:other", "mcp:send", "other/x", false),
		}); err == nil {
			t.Fatal("out-of-subtree delegation must fail")
		}
	})

	t.Run("no grant option → cannot delegate", func(t *testing.T) {
		s := openMem(t)
		addRowFull(t, s, row("folder:acme", "admin", "acme/**", false)) // held WITHOUT option
		if err := delegateForTest(s, "folder:acme", []core.ACLRow{
			row("folder:acme/eng", "mcp:send", "acme/eng", false),
		}); err == nil {
			t.Fatal("delegation without grant option must fail")
		}
	})

	t.Run("cannot broaden the scope glob (F3)", func(t *testing.T) {
		s := openMem(t)
		addRowFull(t, s, row("folder:acme", "admin", "acme/*", true)) // single-segment glob
		// acme/* must NOT cover acme/** (that spans acme/eng/sre the granter lacks).
		if err := delegateForTest(s, "folder:acme", []core.ACLRow{
			row("folder:x", "admin", "acme/**", false),
		}); err == nil {
			t.Fatal("acme/* must not delegate acme/**")
		}
		// But acme/** DOES cover a deeper concrete path and deeper glob.
		addRowFull(t, s, row("folder:acme", "admin", "acme/**", true))
		if err := delegateForTest(s, "folder:acme", []core.ACLRow{
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
		if err := delegateForTest(s, "folder:acme", []core.ACLRow{
			row("folder:x", "admin", "acme/secret", false),
		}); err == nil {
			t.Fatal("must not delegate past own deny")
		}
		// Wildcard principal is an escalation → refused.
		if err := delegateForTest(s, "folder:acme", []core.ACLRow{
			row("*", "admin", "acme/eng", false),
		}); err == nil {
			t.Fatal("must not delegate to a wildcard principal")
		}
	})

	t.Run("cannot broaden the action", func(t *testing.T) {
		s := openMem(t)
		addRowFull(t, s, row("folder:acme", "mcp:send", "acme/**", true)) // only mcp:send
		if err := delegateForTest(s, "folder:acme", []core.ACLRow{
			row("folder:acme/eng", "admin", "acme/eng", false), // admin ⊃ mcp:send
		}); err == nil {
			t.Fatal("delegating a broader action than held must fail")
		}
	})
}

// BUGS L657: a deny the delegator holds blocks any requested row that OVERLAPS it,
// not only one it is covered by; the same holds for a deny reached through a
// wildcard principal, and a held allow with a predicate covers nothing.
func TestDelegateOverlappingDeny(t *testing.T) {
	allow := func(p, a, sc string) core.ACLRow {
		return core.ACLRow{Principal: p, Action: a, Scope: sc, Effect: "allow", GrantOption: true}
	}
	want := core.ACLRow{Principal: "folder:x", Action: "mcp:send", Scope: "a/**", Effect: "allow"}

	t.Run("narrower deny, broader request", func(t *testing.T) {
		s := openMem(t)
		addRowFull(t, s, allow("folder:a", "mcp:send", "a/**"))
		addRowFull(t, s, core.ACLRow{Principal: "folder:a", Action: "mcp:send", Scope: "a/b", Effect: "deny"})
		if err := delegateForTest(s, "folder:a", []core.ACLRow{want}); err == nil {
			t.Fatal("a/** must not be delegated past the delegator's deny on a/b")
		}
	})
	t.Run("narrower-action deny, broader-action request", func(t *testing.T) {
		s := openMem(t)
		addRowFull(t, s, allow("folder:a", "admin", "a/**"))
		addRowFull(t, s, core.ACLRow{Principal: "folder:a", Action: "mcp:send", Scope: "a/b", Effect: "deny"})
		w := want
		w.Action = "admin"
		if err := delegateForTest(s, "folder:a", []core.ACLRow{w}); err == nil {
			t.Fatal("admin must not be delegated past the delegator's mcp:send deny")
		}
	})
	t.Run("wildcard-principal deny", func(t *testing.T) {
		s := openMem(t)
		addRowFull(t, s, allow("folder:a", "mcp:send", "a/**"))
		addRowFull(t, s, core.ACLRow{Principal: "folder:*", Action: "mcp:send", Scope: "a/b", Effect: "deny"})
		if err := delegateForTest(s, "folder:a", []core.ACLRow{want}); err == nil {
			t.Fatal("a wildcard-principal deny that reaches the delegator must block delegation")
		}
	})
	t.Run("unrelated deny does not block", func(t *testing.T) {
		s := openMem(t)
		addRowFull(t, s, allow("folder:a", "mcp:send", "a/**"))
		addRowFull(t, s, core.ACLRow{Principal: "folder:a", Action: "mcp:send", Scope: "z/b", Effect: "deny"})
		if err := delegateForTest(s, "folder:a", []core.ACLRow{want}); err != nil {
			t.Fatalf("disjoint deny must not block: %v", err)
		}
	})
	t.Run("held allow with a predicate covers nothing", func(t *testing.T) {
		s := openMem(t)
		r := allow("folder:a", "mcp:send", "a/**")
		r.Predicate = "tenant=acme"
		addRowFull(t, s, r)
		if err := delegateForTest(s, "folder:a", []core.ACLRow{want}); err == nil {
			t.Fatal("a conditional held row must not delegate an unconditional one")
		}
	})
}

func delegateForTest(s *store.Store, granter string, rows []core.ACLRow) error {
	mutations := make([]auth.Mutation, len(rows))
	for i, row := range rows {
		mutations[i] = auth.Mutation{Row: row}
	}
	_, err := auth.CheckGrant(s, auth.Caller{Principal: granter}, auth.Ask{}, mutations...)
	return err
}

func TestCheckGrantContainment(t *testing.T) {
	row := core.ACLRow{Principal: "user:target", Action: "mcp:X", Scope: "a", Effect: "allow"}
	check := func(t *testing.T, s *store.Store, c auth.Caller, m auth.Mutation, want bool) {
		t.Helper()
		_, err := auth.CheckGrant(s, c, auth.Ask{}, m)
		if (err == nil) != want {
			t.Fatalf("CheckGrant(%+v) = %v, want allowed %v", m, err, want)
		}
	}
	t.Run("sender deny", func(t *testing.T) {
		s := openMem(t)
		addRowFull(t, s, core.ACLRow{Principal: "folder:a", Action: "mcp:X", Scope: "a/**", Effect: "allow", GrantOption: true})
		addRowFull(t, s, core.ACLRow{Principal: "user:sender", Action: "mcp:X", Scope: "a", Effect: "deny"})
		check(t, s, auth.Caller{Principal: "user:sender", Route: "a"}, auth.Mutation{Row: row}, false)
	})
	t.Run("sender local decides", func(t *testing.T) {
		s := openMem(t)
		if err := s.AddMembership("folder:a", "role:operator", "test"); err != nil {
			t.Fatal(err)
		}
		addRowFull(t, s, core.ACLRow{Principal: "user:sender", Action: "mcp:X", Scope: "a", Effect: "allow"})
		check(t, s, auth.Caller{Principal: "user:sender", Route: "a"}, auth.Mutation{Row: row}, false)
	})
	t.Run("recursive deny", func(t *testing.T) {
		s := openMem(t)
		addRowFull(t, s, core.ACLRow{Principal: "user:g", Action: "mcp:X", Scope: "a/**", Effect: "allow", GrantOption: true})
		addRowFull(t, s, core.ACLRow{Principal: "user:g", Action: "mcp:X", Scope: "a/**/x", Effect: "deny"})
		r := row
		r.Scope = "a/b/c/*"
		check(t, s, auth.Caller{Principal: "user:g"}, auth.Mutation{Row: r}, false)
	})
	t.Run("hold needs admin", func(t *testing.T) {
		s := openMem(t)
		r := row
		r.Action = "hold:mcp:X"
		r.Effect = "deny"
		check(t, s, auth.Caller{Principal: "user:g"}, auth.Mutation{Row: r}, false)
		addRowFull(t, s, core.ACLRow{Principal: "user:g", Action: "admin", Scope: "a", Effect: "allow"})
		check(t, s, auth.Caller{Principal: "user:g"}, auth.Mutation{Row: r}, true)
	})
	t.Run("removal reveals default", func(t *testing.T) {
		s := openMem(t)
		if err := s.AddMembership("folder:a", "role:operator", "test"); err != nil {
			t.Fatal(err)
		}
		if err := s.AddMembership("user:operator", "role:operator", "test"); err != nil {
			t.Fatal(err)
		}
		for _, principal := range []string{"user:sender", "google:*", "role:target"} {
			r := row
			r.Principal = principal
			addRowFull(t, s, r)
			m := auth.Mutation{Remove: true, Row: r}
			if principal == "role:target" {
				m = auth.Mutation{Remove: true, Row: core.ACLRow{Principal: "user:target"}, Parent: principal}
			}
			check(t, s, auth.Caller{Principal: "user:sender", Route: "a"}, m, false)
			check(t, s, auth.Caller{Principal: "user:operator"}, m, true)
		}
	})
	t.Run("remove deny", func(t *testing.T) {
		s := openMem(t)
		r := row
		r.Effect = "deny"
		check(t, s, auth.Caller{Principal: "user:g"}, auth.Mutation{Remove: true, Row: r}, false)
	})
	t.Run("operator edge", func(t *testing.T) {
		s := openMem(t)
		check(t, s, auth.Caller{Principal: "user:g"}, auth.Mutation{Row: core.ACLRow{Principal: "user:target"}, Parent: "role:operator"}, false)
	})
	t.Run("empty and wildcard principal", func(t *testing.T) {
		s := openMem(t)
		addRowFull(t, s, core.ACLRow{Principal: "user:g", Action: "mcp:X", Scope: "a", Effect: "allow", GrantOption: true})
		r := row
		r.Principal = ""
		check(t, s, auth.Caller{Principal: "user:g"}, auth.Mutation{Row: r}, true)
		r.Principal = "user:*"
		check(t, s, auth.Caller{Principal: "user:g"}, auth.Mutation{Row: r}, false)
		check(t, s, auth.Caller{Principal: "user:g"}, auth.Mutation{Remove: true, Row: r}, true)
	})
}

func TestCheckGrantDoorAndSecrets(t *testing.T) {
	s := openMem(t)
	caller := auth.Caller{Principal: "user:writer"}
	row := core.ACLRow{Principal: "user:target", Action: "shell:env:KEY", Scope: "a", Effect: "allow"}
	addRowFull(t, s, core.ACLRow{Principal: caller.Principal, Action: "*", Scope: "a/**", Effect: "allow", GrantOption: true})
	_, err := auth.CheckGrant(s, caller, auth.Ask{Action: "mcp:write", Scope: "a"}, auth.Mutation{Row: row})
	if err != nil {
		t.Fatalf("door and secret: %v", err)
	}
	_, err = auth.CheckGrant(s, caller, auth.Ask{Action: "mcp:write", Scope: "outside"}, auth.Mutation{Row: row})
	if err == nil {
		t.Fatal("door outside the held scope must be refused")
	}
}
