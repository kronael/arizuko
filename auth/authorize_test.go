package auth

import (
	"testing"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/store"
)

func openMem(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.OpenMem()
	if err != nil {
		t.Fatalf("OpenMem: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
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
	if ACLAuthorize(s, Caller{Principal: "p"}, "interact", "f", nil) {
		t.Fatal("empty acl should deny")
	}
}

func TestAuthorize_SingleAllow(t *testing.T) {
	s := openMem(t)
	addRow(t, s, "google:alice", "interact", "atlas/eng", "allow")
	if !ACLAuthorize(s, Caller{Principal: "google:alice"}, "interact", "atlas/eng", nil) {
		t.Fatal("expected allow")
	}
}

func TestAuthorize_DenyWinsSameTriple(t *testing.T) {
	s := openMem(t)
	addRow(t, s, "p", "admin", "f", "allow")
	addRow(t, s, "p", "admin", "f", "deny")
	if ACLAuthorize(s, Caller{Principal: "p"}, "admin", "f", nil) {
		t.Fatal("deny must win")
	}
}

func TestAuthorize_RoleMembershipOneDeep(t *testing.T) {
	s := openMem(t)
	addRow(t, s, "role:operator", "*", "**", "allow")
	if err := s.AddMembership("google:alice", "role:operator", ""); err != nil {
		t.Fatal(err)
	}
	if !ACLAuthorize(s, Caller{Principal: "google:alice"}, "admin", "anything", nil) {
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
	if !ACLAuthorize(s, Caller{Principal: "google:bob"}, "interact", "x/y", nil) {
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
	if !ACLAuthorize(s, Caller{Principal: "discord:user/812"}, "admin", "atlas/eng", nil) {
		t.Fatal("JID claim should inherit canonical's grants")
	}
}

func TestAuthorize_WildcardPrincipal(t *testing.T) {
	s := openMem(t)
	addRow(t, s, "discord:user/*", "interact", "atlas/eng", "allow")
	if !ACLAuthorize(s, Caller{Principal: "discord:user/12345"}, "interact", "atlas/eng", nil) {
		t.Fatal("wildcard principal should match concrete user")
	}
	if ACLAuthorize(s, Caller{Principal: "telegram:user/12345"}, "interact", "atlas/eng", nil) {
		t.Fatal("wildcard scoped to discord namespace must not match telegram")
	}
}

func TestAuthorize_WildcardScope(t *testing.T) {
	s := openMem(t)
	addRow(t, s, "p", "interact", "mainco/**", "allow")
	if !ACLAuthorize(s, Caller{Principal: "p"}, "interact", "mainco/eng/sre", nil) {
		t.Fatal("** must cover descendants")
	}
	if ACLAuthorize(s, Caller{Principal: "p"}, "interact", "other/eng", nil) {
		t.Fatal("scope must not match outside subtree")
	}
}

func TestAuthorize_ActionLatticeAdminImpliesMcp(t *testing.T) {
	s := openMem(t)
	addRow(t, s, "p", "admin", "f", "allow")
	if !ACLAuthorize(s, Caller{Principal: "p"}, "mcp:send", "f", nil) {
		t.Fatal("admin should imply mcp:<tool>")
	}
	if !ACLAuthorize(s, Caller{Principal: "p"}, "interact", "f", nil) {
		t.Fatal("admin should imply interact")
	}
}

func TestAuthorize_ActionLatticeStarImpliesAll(t *testing.T) {
	s := openMem(t)
	addRow(t, s, "p", "*", "f", "allow")
	for _, a := range []string{"interact", "admin", "mcp:send", "mcp:reply"} {
		if !ACLAuthorize(s, Caller{Principal: "p"}, a, "f", nil) {
			t.Fatalf("* should imply %s", a)
		}
	}
}

func TestAuthorize_ActionLatticeMcpStar(t *testing.T) {
	s := openMem(t)
	addRow(t, s, "p", "mcp:*", "f", "allow")
	if !ACLAuthorize(s, Caller{Principal: "p"}, "mcp:send", "f", nil) {
		t.Fatal("mcp:* must imply mcp:send")
	}
	if ACLAuthorize(s, Caller{Principal: "p"}, "admin", "f", nil) {
		t.Fatal("mcp:* must NOT imply admin")
	}
}

func TestAuthorize_PredicateMatch(t *testing.T) {
	s := openMem(t)
	addRowFull(t, s, core.ACLRow{
		Principal: "p", Action: "interact", Scope: "f",
		Effect: "allow", Predicate: "discord:guild=G123",
	})
	if !ACLAuthorize(s, Caller{
		Principal: "p", Claims: map[string]string{"discord:guild": "G123"},
	}, "interact", "f", nil) {
		t.Fatal("predicate match should allow")
	}
	if ACLAuthorize(s, Caller{
		Principal: "p", Claims: map[string]string{"discord:guild": "G999"},
	}, "interact", "f", nil) {
		t.Fatal("predicate mismatch should deny")
	}
	if ACLAuthorize(s, Caller{Principal: "p"}, "interact", "f", nil) {
		t.Fatal("missing claim should deny")
	}
}

func TestAuthorize_ChannelBotRoomJID(t *testing.T) {
	s := openMem(t)
	// Room JID carries interact grant for folder F.
	addRow(t, s, "discord:837/1504", "interact", "atlas/eng", "allow")
	// Caller arrives as user, with room JID in Extra.
	caller := Caller{
		Principal: "discord:user/811",
		Extra:     []string{"discord:837/1504"},
	}
	if !ACLAuthorize(s, caller, "interact", "atlas/eng", nil) {
		t.Fatal("room JID grant should reach caller via Extra")
	}
}

func TestAuthorize_ParamGlob(t *testing.T) {
	s := openMem(t)
	addRowFull(t, s, core.ACLRow{
		Principal: "p", Action: "mcp:send", Scope: "f",
		Effect: "allow", Params: "jid=telegram:*",
	})
	if !ACLAuthorize(s, Caller{Principal: "p"}, "mcp:send", "f",
		map[string]string{"jid": "telegram:123"}) {
		t.Fatal("param glob match should allow")
	}
	if ACLAuthorize(s, Caller{Principal: "p"}, "mcp:send", "f",
		map[string]string{"jid": "discord:123"}) {
		t.Fatal("param mismatch should deny")
	}
	if ACLAuthorize(s, Caller{Principal: "p"}, "mcp:send", "f", nil) {
		t.Fatal("missing param should deny")
	}
}

func TestAuthorize_TierDefaultFallback(t *testing.T) {
	s := openMem(t)
	// No acl rows. mcp:send on own folder. Tier 0 = "*".
	caller := Caller{Principal: "folder:atlas/eng"}
	opts := AuthorizeOpts{Folder: "atlas/eng", WorldFolder: "atlas", Tier: 0}
	if !ACLAuthorizeWith(s, caller, "mcp:send", "atlas/eng", nil, opts) {
		t.Fatal("tier 0 default should allow mcp:send")
	}
	// Tier 3 default = reply/send_file/like/edit; no send.
	opts.Tier = 3
	if ACLAuthorizeWith(s, caller, "mcp:send", "atlas/eng", nil, opts) {
		t.Fatal("tier 3 default should NOT allow mcp:send")
	}
	if !ACLAuthorizeWith(s, caller, "mcp:reply", "atlas/eng", nil, opts) {
		t.Fatal("tier 3 default should allow mcp:reply")
	}
	// Non-mcp action never falls back.
	if ACLAuthorizeWith(s, caller, "interact", "atlas/eng", nil, opts) {
		t.Fatal("interact must not fall back to tier defaults")
	}
}

func TestAuthorize_InteractNoTierFallback(t *testing.T) {
	s := openMem(t)
	caller := Caller{Principal: "folder:atlas/eng"}
	opts := AuthorizeOpts{Folder: "atlas/eng", WorldFolder: "atlas", Tier: 0}
	if ACLAuthorizeWith(s, caller, "admin", "atlas/eng", nil, opts) {
		t.Fatal("admin must require an explicit row even with tier 0")
	}
}
