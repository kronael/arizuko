package auth

import "testing"

// These tests pin the CURRENT behavior of AuthorizeStructural for `post` and
// `delete`. The ipc tool descriptions say "Tier 0-2 only" (ipc/ipc.go), but
// AuthorizeStructural routes both through authorizeOutbound, which checks ONLY
// the subtree (target folder == own or descendant) and applies NO tier gate.
// So a tier-3 agent acting on its own folder passes structural authz.
//
// NOT a behavior change — bugs.md flags the desc-vs-enforcement gap (2026-05-28,
// ipc.go:1024/1133). If a tier gate is added (or the desc corrected), these
// expectations move with it. The post/delete direction is permissive; list_acl
// (also "Tier 0-2 only") fails closed for tier 2+ already (asserted below).

func TestAuthorizeStructural_PostDelete_NoTierGate(t *testing.T) {
	// Tier 3: a folder four levels deep (3 slashes → min(3,3)=3).
	tier3 := Identity{Folder: "w/a/b/c", Tier: 3, World: "w"}
	if tier3.Tier != 3 {
		t.Fatalf("precondition: tier = %d, want 3", tier3.Tier)
	}
	for _, tool := range []string{"post", "delete"} {
		// Own folder: passes despite the "Tier 0-2 only" description.
		if err := AuthorizeStructural(tier3, tool, AuthzTarget{TargetFolder: "w/a/b/c"}); err != nil {
			t.Errorf("CURRENT: tier-3 %s on own folder should pass (no tier gate), got %v", tool, err)
		}
		// Descendant: also passes (subtree check only).
		if err := AuthorizeStructural(tier3, tool, AuthzTarget{TargetFolder: "w/a/b/c/d"}); err != nil {
			t.Errorf("CURRENT: tier-3 %s on descendant should pass, got %v", tool, err)
		}
		// Out-of-subtree is still denied (the ONLY structural bound on post/delete).
		if err := AuthorizeStructural(tier3, tool, AuthzTarget{TargetFolder: "w/other"}); err == nil {
			t.Errorf("%s outside subtree must be denied", tool)
		}
	}
}

// Control: list_acl (also documented "Tier 0-2 only") DOES enforce the tier gate
// — tier 2+ is denied. This is the safe (deny) direction; post/delete is the
// permissive gap. Asserting both shows the inconsistency is in post/delete, not
// the tier machinery.
func TestAuthorizeStructural_ListACL_TierGated(t *testing.T) {
	tier2 := Identity{Folder: "w/a/b", Tier: 2, World: "w"}
	if err := AuthorizeStructural(tier2, "list_acl", AuthzTarget{TargetFolder: "w/a/b"}); err == nil {
		t.Error("list_acl must deny tier 2 (Tier 0-1 only) — tier gate present here")
	}
}
