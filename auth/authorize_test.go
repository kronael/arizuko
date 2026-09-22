// Package auth_test (external) so these tests can build their acl and
// acl_membership fixture from ROUTD's own tables, which is the schema
// production has — proxyd, webd, dashd, onbod and the CLI all reach those two
// tables through store. routd imports auth, so an INTERNAL auth test file
// cannot import routd back (a cycle at the test-binary level); the external
// test package convention is exactly the escape hatch for that, as
// resreg/archive_test.go documents for the same reason.
package auth_test

import (
	"testing"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore/dynstore/dyntest"
	"github.com/kronael/arizuko/store"
	"github.com/kronael/arizuko/tests/testutils"
)

func TestMain(m *testing.M) { dyntest.Main(m) }

// openMem is routd's own tables, wrapped in the shared store library. It
// arrives already seeded with role:member (the 12 messaging verbs) and
// role:operator (*, ** WITH GRANT OPTION), which is what a real acl table holds
// before anyone grants anything.
func openMem(t *testing.T) *store.Store {
	t.Helper()
	return testutils.NewRoutdStore(t)
}

func addRow(t *testing.T, s *store.Store, principal, action, scope, effect string) {
	t.Helper()
	if err := s.AddACLRow(core.ACLRow{
		Principal: principal, Action: action, Scope: scope, Effect: effect,
	}); err != nil {
		t.Fatalf("AddACLRow: %v", err)
	}
}

func addRowFull(t *testing.T, s *store.Store, r core.ACLRow) {
	t.Helper()
	if err := s.AddACLRow(r); err != nil {
		t.Fatalf("AddACLRow: %v", err)
	}
}

func TestAuthorize_EmptyDenies(t *testing.T) {
	s := openMem(t)
	if auth.Authorize(s, auth.Caller{Principal: "p"}, "interact", "f", nil) {
		t.Fatal("a principal with no row of its own must be denied, seeded roles or not")
	}
}

func TestAuthorize_SingleAllow(t *testing.T) {
	s := openMem(t)
	addRow(t, s, "google:alice", "interact", "atlas/eng", "allow")
	if !auth.Authorize(s, auth.Caller{Principal: "google:alice"}, "interact", "atlas/eng", nil) {
		t.Fatal("expected allow")
	}
}

func TestAuthorize_DenyWinsSameTriple(t *testing.T) {
	s := openMem(t)
	addRow(t, s, "p", "admin", "f", "allow")
	addRow(t, s, "p", "admin", "f", "deny")
	if auth.Authorize(s, auth.Caller{Principal: "p"}, "admin", "f", nil) {
		t.Fatal("deny must win")
	}
}

func TestAuthorize_RoleMembershipOneDeep(t *testing.T) {
	s := openMem(t)
	addRow(t, s, "role:operator", "*", "**", "allow")
	if err := s.AddMembership("google:alice", "role:operator", ""); err != nil {
		t.Fatal(err)
	}
	if !auth.Authorize(s, auth.Caller{Principal: "google:alice"}, "admin", "anything", nil) {
		t.Fatal("operator role should grant admin via membership")
	}
}

func TestAuthorize_RoleMembershipThreeDeep(t *testing.T) {
	s := openMem(t)
	addRow(t, s, "role:operator", "*", "**", "allow")
	if err := s.AddMembership("role:senior", "role:operator", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.AddMembership("role:eng", "role:senior", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.AddMembership("google:bob", "role:eng", ""); err != nil {
		t.Fatal(err)
	}
	if !auth.Authorize(s, auth.Caller{Principal: "google:bob"}, "interact", "x/y", nil) {
		t.Fatal("3-deep role chain should grant")
	}
}

func TestAuthorize_JIDClaimViaMembership(t *testing.T) {
	s := openMem(t)
	addRow(t, s, "google:alice", "admin", "atlas/**", "allow")
	// discord JID claims canonical sub.
	if err := s.AddMembership("discord:user/812", "google:alice", ""); err != nil {
		t.Fatal(err)
	}
	if !auth.Authorize(s, auth.Caller{Principal: "discord:user/812"}, "admin", "atlas/eng", nil) {
		t.Fatal("JID claim should inherit canonical's grants")
	}
}

func TestAuthorize_WildcardPrincipal(t *testing.T) {
	s := openMem(t)
	addRow(t, s, "discord:user/*", "interact", "atlas/eng", "allow")
	if !auth.Authorize(s, auth.Caller{Principal: "discord:user/12345"}, "interact", "atlas/eng", nil) {
		t.Fatal("wildcard principal should match concrete user")
	}
	if auth.Authorize(s, auth.Caller{Principal: "telegram:user/12345"}, "interact", "atlas/eng", nil) {
		t.Fatal("wildcard scoped to discord namespace must not match telegram")
	}
}

func TestAuthorize_WildcardScope(t *testing.T) {
	s := openMem(t)
	addRow(t, s, "p", "interact", "mainco/**", "allow")
	if !auth.Authorize(s, auth.Caller{Principal: "p"}, "interact", "mainco/eng/sre", nil) {
		t.Fatal("** must cover descendants")
	}
	if auth.Authorize(s, auth.Caller{Principal: "p"}, "interact", "other/eng", nil) {
		t.Fatal("scope must not match outside subtree")
	}
}

func TestAuthorize_ActionLatticeAdminImpliesMcp(t *testing.T) {
	s := openMem(t)
	addRow(t, s, "p", "admin", "f", "allow")
	if !auth.Authorize(s, auth.Caller{Principal: "p"}, "mcp:send", "f", nil) {
		t.Fatal("admin should imply mcp:<tool>")
	}
	if !auth.Authorize(s, auth.Caller{Principal: "p"}, "interact", "f", nil) {
		t.Fatal("admin should imply interact")
	}
}

func TestAuthorize_ActionLatticeStarImpliesAll(t *testing.T) {
	s := openMem(t)
	addRow(t, s, "p", "*", "f", "allow")
	for _, a := range []string{"interact", "admin", "mcp:send", "mcp:reply"} {
		if !auth.Authorize(s, auth.Caller{Principal: "p"}, a, "f", nil) {
			t.Fatalf("* should imply %s", a)
		}
	}
}

func TestAuthorize_ActionLatticeMcpStar(t *testing.T) {
	s := openMem(t)
	addRow(t, s, "p", "mcp:*", "f", "allow")
	if !auth.Authorize(s, auth.Caller{Principal: "p"}, "mcp:send", "f", nil) {
		t.Fatal("mcp:* must imply mcp:send")
	}
	if auth.Authorize(s, auth.Caller{Principal: "p"}, "admin", "f", nil) {
		t.Fatal("mcp:* must NOT imply admin")
	}
}

func TestAuthorize_PredicateMatch(t *testing.T) {
	s := openMem(t)
	addRowFull(t, s, core.ACLRow{
		Principal: "p", Action: "interact", Scope: "f",
		Effect: "allow", Predicate: "discord:guild=G123",
	})
	if !auth.Authorize(s, auth.Caller{
		Principal: "p", Claims: map[string]string{"discord:guild": "G123"},
	}, "interact", "f", nil) {
		t.Fatal("predicate match should allow")
	}
	if auth.Authorize(s, auth.Caller{
		Principal: "p", Claims: map[string]string{"discord:guild": "G999"},
	}, "interact", "f", nil) {
		t.Fatal("predicate mismatch should deny")
	}
	if auth.Authorize(s, auth.Caller{Principal: "p"}, "interact", "f", nil) {
		t.Fatal("missing claim should deny")
	}
}

func TestAuthorize_ChannelBotRoomJID(t *testing.T) {
	s := openMem(t)
	// Room JID carries interact grant for folder F.
	addRow(t, s, "discord:837/1504", "interact", "atlas/eng", "allow")
	// Caller arrives as user, with room JID in Extra.
	caller := auth.Caller{
		Principal: "discord:user/811",
		Extra:     []string{"discord:837/1504"},
	}
	if !auth.Authorize(s, caller, "interact", "atlas/eng", nil) {
		t.Fatal("room JID grant should reach caller via Extra")
	}
}

func TestAuthorize_ParamGlob(t *testing.T) {
	s := openMem(t)
	addRowFull(t, s, core.ACLRow{
		Principal: "p", Action: "mcp:send", Scope: "f",
		Effect: "allow", Params: "jid=telegram:*",
	})
	if !auth.Authorize(s, auth.Caller{Principal: "p"}, "mcp:send", "f",
		map[string]string{"jid": "telegram:123"}) {
		t.Fatal("param glob match should allow")
	}
	if auth.Authorize(s, auth.Caller{Principal: "p"}, "mcp:send", "f",
		map[string]string{"jid": "discord:123"}) {
		t.Fatal("param mismatch should deny")
	}
	if auth.Authorize(s, auth.Caller{Principal: "p"}, "mcp:send", "f", nil) {
		t.Fatal("missing param should deny")
	}
}

// TestAuthorizeWith_NoTierFallback pins 5/33-paths-roles phase e: the DeriveRules(tier) fallback
// is deleted — magnitude comes from role membership (DATA), never a recomputed tier
// int. A folder with no matching allow row (no membership) is DENIED, even for mcp:*.
func TestAuthorizeWith_NoTierFallback(t *testing.T) {
	s := openMem(t)
	caller := auth.Caller{Principal: "folder:atlas/eng"}
	// No rows, no membership → deny (there is no tier default to fall back to).
	if auth.Authorize(s, caller, "mcp:send", "atlas/eng", nil) {
		t.Fatal("no fallback: mcp:send with no matching row must be denied")
	}
	// An explicit allow row grants it — the data path.
	if err := s.PutACLRow(core.ACLRow{
		Principal: "folder:atlas/eng", Action: "mcp:send", Scope: "atlas/eng", Effect: "allow",
	}); err != nil {
		t.Fatal(err)
	}
	if !auth.Authorize(s, caller, "mcp:send", "atlas/eng", nil) {
		t.Fatal("explicit allow row must grant mcp:send")
	}
}

func TestAuthorize_InteractNoTierFallback(t *testing.T) {
	s := openMem(t)
	caller := auth.Caller{Principal: "folder:atlas/eng"}
	if auth.Authorize(s, caller, "admin", "atlas/eng", nil) {
		t.Fatal("admin must require an explicit row (no tier default)")
	}
}
