package auth

import (
	"testing"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/store"
)

func scopesEnv(t *testing.T, rows ...core.ACLRow) *store.Store {
	t.Helper()
	s, err := store.OpenMem()
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
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
	eqScopes(t, UserScopes(scopesEnv(t), "nobody"))
}

func TestUserScopesEmptyInputs(t *testing.T) {
	eqScopes(t, UserScopes(scopesEnv(t), ""))
	eqScopes(t, UserScopes(nil, "somebody"))
}

func TestUserScopesSpecificFolders(t *testing.T) {
	s := scopesEnv(t,
		core.ACLRow{Principal: "u1", Action: "admin", Scope: "beta", Effect: "allow"},
		core.ACLRow{Principal: "u1", Action: "admin", Scope: "alpha", Effect: "allow"},
	)
	s.CreateAuthUser("u1", "u1", "User One")
	eqScopes(t, UserScopes(s, "u1"), "alpha", "beta")
}

func TestUserScopesOperatorViaMembership(t *testing.T) {
	s := scopesEnv(t,
		core.ACLRow{Principal: "role:operator", Action: "*", Scope: "**", Effect: "allow"},
	)
	s.CreateAuthUser("op", "op", "Operator")
	if err := s.AddMembership("op", "role:operator", "test"); err != nil {
		t.Fatal(err)
	}
	eqScopes(t, UserScopes(s, "op"), "**")
}

// The bug that came with the raw `principal IN (...)` query in store/: a
// wildcard-principal grant is a row Authorize honours, so a caller who holds
// only that grant authorizes fine yet had an EMPTY X-User-Groups header — the
// listing and the gate disagreed about which rows exist.
func TestUserScopesIncludesWildcardPrincipalGrants(t *testing.T) {
	s := scopesEnv(t,
		core.ACLRow{Principal: "google:*", Action: "admin", Scope: "atlas", Effect: "allow"},
	)
	eqScopes(t, UserScopes(s, "google:114"), "atlas")

	// Authorize agrees — that is the point of reading one row set.
	if !Authorize(s, Caller{Principal: "google:114"}, "admin", "atlas", nil) {
		t.Error("Authorize must honour the same wildcard-principal row")
	}
	// The glob is segment-wise on ':', so another namespace gets nothing.
	eqScopes(t, UserScopes(s, "github:114"))
}

func TestUserScopesExcludesDenyOnlyRows(t *testing.T) {
	s := scopesEnv(t,
		core.ACLRow{Principal: "u1", Action: "admin", Scope: "alpha", Effect: "allow"},
		core.ACLRow{Principal: "u1", Action: "admin", Scope: "blocked", Effect: "deny"},
	)
	eqScopes(t, UserScopes(s, "u1"), "alpha")
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
	eqScopes(t, UserScopes(s, "u1"), "alpha")
	if Authorize(s, Caller{Principal: "u1"}, "admin", "alpha", nil) {
		t.Error("deny must win at the gate even though the scope still lists")
	}
}

// Same for action: the list says nothing about WHICH action was granted.
func TestUserScopesIgnoresActionSoTheGateMustNot(t *testing.T) {
	s := scopesEnv(t,
		core.ACLRow{Principal: "u1", Action: "mcp:reply", Scope: "alpha", Effect: "allow"},
	)
	eqScopes(t, UserScopes(s, "u1"), "alpha")
	if Authorize(s, Caller{Principal: "u1"}, "admin", "alpha", nil) {
		t.Error("mcp:reply must not authorize admin")
	}
}

// A broken acl table yields no scopes rather than a partial or panicking read —
// the row loaders fail closed and UserScopes inherits that.
func TestUserScopesFailsClosedOnBrokenTable(t *testing.T) {
	s := scopesEnv(t)
	if _, err := s.DB().Exec(`DROP TABLE acl`); err != nil {
		t.Fatalf("drop acl: %v", err)
	}
	eqScopes(t, UserScopes(s, "google:alice"))
}
