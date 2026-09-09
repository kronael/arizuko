package auth_test

import (
	"testing"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/store"
)

func scopesEnv(t *testing.T, rows ...core.ACLRow) *store.Store {
	t.Helper()
	s := openMem(t)
	for _, r := range rows {
		if err := s.AddACLRow(r); err != nil {
			t.Fatalf("seed %+v: %v", r, err)
		}
	}
	return s
}

func eqScopes(t *testing.T, got []string, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("UserScopes = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("UserScopes = %v, want %v", got, want)
		}
	}
}

func TestUserScopesNoRows(t *testing.T) {
	eqScopes(t, mustScopes(auth.UserScopes(scopesEnv(t), "nobody")))
}

func TestUserScopesEmptyInputs(t *testing.T) {
	eqScopes(t, mustScopes(auth.UserScopes(scopesEnv(t), "")))
	eqScopes(t, mustScopes(auth.UserScopes(nil, "somebody")))
}

func TestUserScopesSpecificFolders(t *testing.T) {
	s := scopesEnv(t,
		core.ACLRow{Principal: "u1", Action: "admin", Scope: "beta", Effect: "allow"},
		core.ACLRow{Principal: "u1", Action: "admin", Scope: "alpha", Effect: "allow"},
	)
	s.CreateUserProfile("u1", "u1", "User One")
	eqScopes(t, mustScopes(auth.UserScopes(s, "u1")), "alpha", "beta")
}

func TestUserScopesOperatorViaMembership(t *testing.T) {
	s := scopesEnv(t,
		core.ACLRow{Principal: "role:operator", Action: "*", Scope: "**", Effect: "allow"},
	)
	s.CreateUserProfile("op", "op", "Operator")
	if err := s.AddMembership("op", "role:operator", "test"); err != nil {
		t.Fatal(err)
	}
	eqScopes(t, mustScopes(auth.UserScopes(s, "op")), "**")
}

// The bug that came with the raw `principal IN (...)` query in store/: a
// wildcard-principal grant is a row Authorize honours, so a caller who holds
// only that grant authorizes fine yet had an EMPTY X-User-Groups header — the
// listing and the gate disagreed about which rows exist.
func TestUserScopesIncludesWildcardPrincipalGrants(t *testing.T) {
	s := scopesEnv(t,
		core.ACLRow{Principal: "google:*", Action: "admin", Scope: "atlas", Effect: "allow"},
	)
	eqScopes(t, mustScopes(auth.UserScopes(s, "google:114")), "atlas")

	// Authorize agrees — that is the point of reading one row set.
	if !auth.Authorize(s, auth.Caller{Principal: "google:114"}, "admin", "atlas", nil) {
		t.Error("Authorize must honour the same wildcard-principal row")
	}
	// The glob is segment-wise on ':', so another namespace gets nothing.
	eqScopes(t, mustScopes(auth.UserScopes(s, "github:114")))
}

func TestUserScopesExcludesDenyOnlyRows(t *testing.T) {
	s := scopesEnv(t,
		core.ACLRow{Principal: "u1", Action: "admin", Scope: "alpha", Effect: "allow"},
		core.ACLRow{Principal: "u1", Action: "admin", Scope: "blocked", Effect: "deny"},
	)
	eqScopes(t, mustScopes(auth.UserScopes(s, "u1")), "alpha")
}

// A []string cannot carry a deny, so a scope with BOTH an allow and a deny row
// still lists. This is the structural reason UserScopes may not gate: only
// Authorize sees the deny. If this test ever has to change, the listing has
// been made load-bearing and something is now deciding access from it.
func TestUserScopesCannotRepresentDenyAndSoCannotGate(t *testing.T) {
	s := scopesEnv(t,
		core.ACLRow{Principal: "u1", Action: "*", Scope: "alpha", Effect: "allow"},
		core.ACLRow{Principal: "u1", Action: "admin", Scope: "alpha", Effect: "deny"},
	)
	eqScopes(t, mustScopes(auth.UserScopes(s, "u1")), "alpha")
	if auth.Authorize(s, auth.Caller{Principal: "u1"}, "admin", "alpha", nil) {
		t.Error("deny must win at the gate even though the scope still lists")
	}
}

// Same for action: the list says nothing about WHICH action was granted.
func TestUserScopesIgnoresActionSoTheGateMustNot(t *testing.T) {
	s := scopesEnv(t,
		core.ACLRow{Principal: "u1", Action: "mcp:reply", Scope: "alpha", Effect: "allow"},
	)
	eqScopes(t, mustScopes(auth.UserScopes(s, "u1")), "alpha")
	if auth.Authorize(s, auth.Caller{Principal: "u1"}, "admin", "alpha", nil) {
		t.Error("mcp:reply must not authorize admin")
	}
}

// A broken acl table yields an ERROR, not an empty scope list (BUGS A4). It used
// to return no scopes, which is fail-closed for the evaluator but reads as "this
// user holds nothing" to everyone else — authd snapshots this at login, so an
// unreadable policy would have minted a scopeless session indistinguishable from
// a legitimately unprivileged one.
func TestUserScopesFailsClosedOnBrokenTable(t *testing.T) {
	s := scopesEnv(t)
	if _, err := s.DB().Exec(`DROP TABLE acl`); err != nil {
		t.Fatalf("drop acl: %v", err)
	}
	got, err := auth.UserScopes(s, "google:alice")
	if err == nil {
		t.Error("a broken acl table must surface an error, not an empty scope list")
	}
	if len(got) != 0 {
		t.Errorf("UserScopes on a broken table = %v, want no scopes alongside the error", got)
	}
}

// Authorize still DENIES on the same broken table — the error return changes who
// can SEE the failure, never the direction it fails in.
func TestAuthorizeDeniesOnBrokenTable(t *testing.T) {
	s := scopesEnv(t, core.ACLRow{
		Principal: "u1", Action: "*", Scope: "**", Effect: "allow",
	})
	if !auth.Authorize(s, auth.Caller{Principal: "u1"}, "admin", "alpha", nil) {
		t.Fatal("fixture is vacuous: the grant does not authorize before the drop")
	}
	if _, err := s.DB().Exec(`DROP TABLE acl`); err != nil {
		t.Fatalf("drop acl: %v", err)
	}
	if auth.Authorize(s, auth.Caller{Principal: "u1"}, "admin", "alpha", nil) {
		t.Error("Authorize allowed with an unreadable policy")
	}
	if auth.EffectiveActions(s, auth.Caller{Principal: "u1"})("mcp:reply") {
		t.Error("EffectiveActions advertised a tool with an unreadable policy")
	}
}

// Delegate REFUSES rather than silently finding nothing held: it is the
// precondition on a write that hands out authority, so an unreadable policy
// means nothing is provably a subset of what the granter holds.
func TestDelegateRefusesOnBrokenTable(t *testing.T) {
	s := scopesEnv(t, core.ACLRow{
		Principal: "u1", Action: "*", Scope: "**", Effect: "allow", GrantOption: true,
	})
	want := []core.ACLRow{{Principal: "u2", Action: "mcp:reply", Scope: "alpha", Effect: "allow"}}
	if err := auth.Delegate(s, "u1", want); err != nil {
		t.Fatalf("fixture is vacuous: delegation already refused before the drop: %v", err)
	}
	if _, err := s.DB().Exec(`DROP TABLE acl`); err != nil {
		t.Fatalf("drop acl: %v", err)
	}
	if err := auth.Delegate(s, "u1", want); err == nil {
		t.Error("Delegate allowed a grant with an unreadable policy")
	}
}

// mustScopes unwraps UserScopes in a test that is not about read failures; the
// error path itself is asserted in store/failclosed_test.go.
func mustScopes(scopes []string, err error) []string {
	if err != nil {
		panic("UserScopes: " + err.Error())
	}
	return scopes
}
