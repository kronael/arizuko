package auth_test

import (
	"testing"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/store"
)

// The two-axis tests (spec 5/33-paths-roles §"Two axes"). Caller.Route carries what reaching
// through a destination confers; Caller.Principal carries what the attested sender
// holds, confined to Route's subtree with the lattice refused.

// seedFolder gives folder F exactly what a real one has at birth: membership in
// role:member, whose rows are the 12 messaging verbs at scope `**`.
func seedFolder(t *testing.T, s *store.Store, folder string) {
	t.Helper()
	addRow(t, s, "role:member", "mcp:send", "**", "allow")
	addRow(t, s, "role:member", "mcp:reply", "**", "allow")
	if err := s.PutMembership(auth.FolderPrincipal(folder), "role:member", "test"); err != nil {
		t.Fatalf("PutMembership: %v", err)
	}
}

// TestAxes_UnpairedSenderIsUnchanged is the non-negotiable: krons carries 164
// distinct message senders, ONE pairing token and ZERO acl principals that are not
// `folder:` or `role:` (BUGS L146), so 163 of 164 senders match no row. Their turns
// must authorize exactly as they did before the identity axis existed.
//
// Falsifiable: drop Route from expandPrincipals' frontier and the folder loses its
// role:member rows, so every case flips to deny.
func TestAxes_UnpairedSenderIsUnchanged(t *testing.T) {
	s := openMem(t)
	seedFolder(t, s, "atlas/eng")
	addRow(t, s, auth.FolderPrincipal("atlas/eng"), "mcp:register_group", "atlas/eng/**", "allow")

	before := auth.Caller{Principal: auth.FolderPrincipal("atlas/eng")}
	after := auth.Caller{Principal: "telegram:user/1112184352", Route: "atlas/eng"}

	cases := []struct{ action, scope string }{
		{"mcp:send", "atlas/eng"},
		{"mcp:send", "other/tenant"},
		{"mcp:register_group", "atlas/eng/child"},
		{"mcp:register_group", "atlas/eng"},
		{"mcp:register_group", "other/tenant"},
		{"admin", "atlas/eng"},
		{"mcp:schedule_task", "atlas/eng"},
	}
	for _, c := range cases {
		want := auth.Authorize(s, before, c.action, c.scope, nil)
		got := auth.Authorize(s, after, c.action, c.scope, nil)
		if got != want {
			t.Fatalf("%s on %s: unpaired sender got %v, folder-only %v — the axis must be inert for a sender with no rows",
				c.action, c.scope, got, want)
		}
	}
}

// TestAxes_SenderGrantStopsAtTheSubtree is the hazard. A person holding a grant in
// tenant X must not lend it to tenant Y's agent by speaking in Y's chat — the
// confused deputy the naive union builds.
//
// Falsifiable: delete the withinSubtree guard from Authorize and the cross-tenant
// case allows.
func TestAxes_SenderGrantStopsAtTheSubtree(t *testing.T) {
	s := openMem(t)
	seedFolder(t, s, "tenantY")
	addRow(t, s, "google:alice", "mcp:register_group", "tenantX/**", "allow")
	addRow(t, s, "google:alice", "mcp:register_group", "tenantY/**", "allow")

	turn := auth.Caller{Principal: "google:alice", Route: "tenantY"}
	if auth.Authorize(s, turn, "mcp:register_group", "tenantX/child", nil) {
		t.Fatal("a speaker's grant on ANOTHER tenant must not reach through this turn")
	}
	if !auth.Authorize(s, turn, "mcp:register_group", "tenantY/child", nil) {
		t.Fatal("a speaker's grant INSIDE the turn's subtree is the feature; it must apply")
	}
	// The bound is the turn's folder, not the row's: the same row read from a turn
	// routed elsewhere confers nothing.
	elsewhere := auth.Caller{Principal: "google:alice", Route: "tenantZ"}
	if auth.Authorize(s, elsewhere, "mcp:register_group", "tenantY/child", nil) {
		t.Fatal("identity axis must be bounded by the TURN's folder, not by the row's scope")
	}
}

// TestAxes_NoReachOutsideTheSubtreeWhoeverSpeaks states the invariant directly:
// outside the turn's own subtree the verdict does not depend on the sender at all.
// It is the general form of the case above — every action, every speaker.
//
// Falsifiable: same guard. Removing it lets the operator row answer for `other/tenant`.
func TestAxes_NoReachOutsideTheSubtreeWhoeverSpeaks(t *testing.T) {
	s := openMem(t)
	seedFolder(t, s, "atlas/eng")
	addRow(t, s, "role:operator", "*", "**", "allow")
	if err := s.PutMembership("google:root", "role:operator", "test"); err != nil {
		t.Fatal(err)
	}
	addRow(t, s, "google:bob", "mcp:register_group", "**", "allow")

	folderOnly := auth.Caller{Principal: auth.FolderPrincipal("atlas/eng")}
	speakers := []string{"", "google:root", "google:bob", "telegram:user/999", "service:routd"}
	outside := []string{"other/tenant", "atlas", "atlas/engineering", "**", "atlas/eng2"}
	actions := []string{"mcp:send", "mcp:register_group", "admin", "interact"}

	for _, sp := range speakers {
		for _, scope := range outside {
			for _, action := range actions {
				want := auth.Authorize(s, folderOnly, action, scope, nil)
				got := auth.Authorize(s, auth.Caller{Principal: sp, Route: "atlas/eng"}, action, scope, nil)
				if got != want {
					t.Fatalf("speaker %q, %s on %s (outside atlas/eng): got %v, want the folder's own %v",
						sp, action, scope, got, want)
				}
			}
		}
	}
}

// TestAxes_LatticeIsNotConferredByIdentity pins the second bound. An operator holds
// (`*`, `**`); if actionCovers read that row on the identity axis, every message an
// operator sent would silently raise the turn that answered it — which is what the
// explicit, one-shot `/root` (routd.cmdRoot, gated on IsOperator) is for.
//
// Falsifiable: swap identityCovers for actionCovers and the management call allows.
func TestAxes_LatticeIsNotConferredByIdentity(t *testing.T) {
	s := openMem(t)
	seedFolder(t, s, "atlas/eng")
	addRow(t, s, "role:operator", "*", "**", "allow")
	if err := s.PutMembership("google:root", "role:operator", "test"); err != nil {
		t.Fatal(err)
	}

	turn := auth.Caller{Principal: "google:root", Route: "atlas/eng"}
	if auth.Authorize(s, turn, "mcp:register_group", "atlas/eng", nil) {
		t.Fatal("an operator merely SPEAKING must not elevate the turn; /root is the explicit path")
	}
	// The same operator's row still answers on its own axis, unchanged.
	if !auth.Authorize(s, auth.Caller{Principal: "google:root"}, "mcp:register_group", "atlas/eng", nil) {
		t.Fatal("the operator's own single-axis authority must be untouched")
	}
	// An explicitly delegated tool row still confers — that is the whole feature.
	addRow(t, s, "google:root", "mcp:register_group", "atlas/**", "allow")
	if !auth.Authorize(s, turn, "mcp:register_group", "atlas/eng", nil) {
		t.Fatal("an EXPLICIT mcp:<tool> row on the sender must confer inside the subtree")
	}
}

// TestAxes_DenyWinsAcrossBothAxes: a deny is never confined. A person barred from a
// verb bars the turn they trigger — the only NARROWING a union of grants can express.
//
// Falsifiable: confine the deny branch the way the allow branch is confined, or gate
// it behind anyPrincipalMatches, and either half stops refusing.
func TestAxes_DenyWinsAcrossBothAxes(t *testing.T) {
	s := openMem(t)
	seedFolder(t, s, "atlas/eng")

	// Deny on the IDENTITY axis beats the route's own allow.
	addRow(t, s, "google:mallory", "mcp:send", "**", "deny")
	if auth.Authorize(s, auth.Caller{Principal: "google:mallory", Route: "atlas/eng"}, "mcp:send", "atlas/eng", nil) {
		t.Fatal("a deny on the sender must refuse the turn it triggered")
	}
	if !auth.Authorize(s, auth.Caller{Principal: "google:innocent", Route: "atlas/eng"}, "mcp:send", "atlas/eng", nil) {
		t.Fatal("the deny must be that sender's, not the folder's")
	}

	// Deny on the ROUTE axis beats an identity allow inside the subtree.
	addRow(t, s, auth.FolderPrincipal("atlas/eng"), "mcp:register_group", "**", "deny")
	addRow(t, s, "google:carol", "mcp:register_group", "atlas/**", "allow")
	if auth.Authorize(s, auth.Caller{Principal: "google:carol", Route: "atlas/eng"}, "mcp:register_group", "atlas/eng", nil) {
		t.Fatal("a deny on the route must refuse an allow the sender brought")
	}

	// A deny reached through the LATTICE still refuses, even though an identity
	// ALLOW may not use it: `*` deny covers mcp:<tool>.
	addRow(t, s, "google:dave", "*", "**", "deny")
	if auth.Authorize(s, auth.Caller{Principal: "google:dave", Route: "atlas/eng"}, "mcp:reply", "atlas/eng", nil) {
		t.Fatal("a lattice deny on the sender must still refuse")
	}
}

// TestAxes_GlobScopeIsNeverInsideASubtree: routd authorizes package management
// against the literal scope `**` (installedPackagesScope), so a pattern target must
// never be argued into a subtree — that would hand the widest grant in the tree to
// anyone who speaks.
//
// Falsifiable: drop the ContainsAny check from withinSubtree and `**` passes.
func TestAxes_GlobScopeIsNeverInsideASubtree(t *testing.T) {
	s := openMem(t)
	seedFolder(t, s, "atlas")
	addRow(t, s, "google:alice", "mcp:install_package", "**", "allow")
	turn := auth.Caller{Principal: "google:alice", Route: "atlas"}
	for _, scope := range []string{"**", "atlas/*", "atlas/**", "atlas/?"} {
		if auth.Authorize(s, turn, "mcp:install_package", scope, nil) {
			t.Fatalf("scope %q is a pattern, not a target inside atlas", scope)
		}
	}
}

// TestAxes_SubtreeBoundIsSegmentwise: `atlas` does not cover `atlas-eng`, and the
// compare is case-sensitive. The SQL LIKE form of this test is the one that widened
// six read paths (BUGS L122); this one must not repeat it.
//
// Falsifiable: write withinSubtree as a bare HasPrefix(scope, folder) and
// `atlas-eng` passes.
func TestAxes_SubtreeBoundIsSegmentwise(t *testing.T) {
	s := openMem(t)
	seedFolder(t, s, "atlas")
	addRow(t, s, "google:alice", "mcp:register_group", "**", "allow")
	turn := auth.Caller{Principal: "google:alice", Route: "atlas"}

	if !auth.Authorize(s, turn, "mcp:register_group", "atlas", nil) {
		t.Fatal("the folder itself is inside its own subtree")
	}
	if !auth.Authorize(s, turn, "mcp:register_group", "atlas/eng/deep", nil) {
		t.Fatal("a descendant is inside")
	}
	for _, scope := range []string{"atlas-eng", "atlasx", "ATLAS/eng", "atlas2/eng"} {
		if auth.Authorize(s, turn, "mcp:register_group", scope, nil) {
			t.Fatalf("%q is not inside atlas", scope)
		}
	}
}

// TestAxes_SingleAxisCallersUnchanged pins that every caller that sets no Route —
// dashd, UserScopes, IsOperator, operator REST — evaluates on the old rules,
// lattice and all.
//
// Falsifiable: make the confinement unconditional (drop the `caller.Route == ""`
// arm) and the operator's lattice row stops answering.
func TestAxes_SingleAxisCallersUnchanged(t *testing.T) {
	s := openMem(t)
	addRow(t, s, "role:operator", "*", "**", "allow")
	if err := s.PutMembership("google:root", "role:operator", "test"); err != nil {
		t.Fatal(err)
	}
	c := auth.Caller{Principal: "google:root"}
	for _, tc := range []struct{ action, scope string }{
		{"admin", "anything"}, {"mcp:register_group", "a/b/c"}, {"interact", "x"},
	} {
		if !auth.Authorize(s, c, tc.action, tc.scope, nil) {
			t.Fatalf("single-axis operator lost %s on %s", tc.action, tc.scope)
		}
	}
	// Extra keeps its meaning: more of the same identity, unconfined.
	addRow(t, s, "discord:837/1504", "interact", "atlas/eng", "allow")
	if !auth.Authorize(s, auth.Caller{Principal: "discord:user/811", Extra: []string{"discord:837/1504"}},
		"interact", "atlas/eng", nil) {
		t.Fatal("Extra must still fold in unconfined when no Route is set")
	}
}

// TestAxes_RouteAloneAuthorizes: a turn whose sender resolves to nothing (an empty
// trigger) still runs on the route's authority. Before the axis the empty-principal
// guard denied outright, which would have stopped every system-triggered turn.
//
// Falsifiable: restore `caller.Principal == ""` as an unconditional deny.
func TestAxes_RouteAloneAuthorizes(t *testing.T) {
	s := openMem(t)
	seedFolder(t, s, "atlas/eng")
	if !auth.Authorize(s, auth.Caller{Route: "atlas/eng"}, "mcp:send", "atlas/eng", nil) {
		t.Fatal("the route axis alone must authorize the folder's own floor")
	}
	if auth.Authorize(s, auth.Caller{}, "mcp:send", "atlas/eng", nil) {
		t.Fatal("no axis at all must still deny")
	}
}

// TestAxes_VisibilityFollowsTheSameRule: EffectiveActions is what registers a tool
// at all, so the identity axis is inert unless it reads Route too — and it must
// refuse the lattice there for the same reason Authorize does.
//
// Falsifiable: drop the identity arm from EffectiveActions' loop and the delegated
// tool stops being listed; let `*` set lattice there and the operator sees every tool.
func TestAxes_VisibilityFollowsTheSameRule(t *testing.T) {
	s := openMem(t)
	seedFolder(t, s, "atlas/eng")
	addRow(t, s, "role:operator", "*", "**", "allow")
	if err := s.PutMembership("google:root", "role:operator", "test"); err != nil {
		t.Fatal(err)
	}
	addRow(t, s, "google:carol", "mcp:register_group", "atlas/**", "allow")

	byOperator := auth.EffectiveActions(s, auth.Caller{Principal: "google:root", Route: "atlas/eng"})
	if byOperator("mcp:register_group") {
		t.Fatal("an operator speaking must not make every tool visible; that is /root's job")
	}
	if !byOperator("mcp:send") {
		t.Fatal("the folder's own floor must stay visible")
	}
	byCarol := auth.EffectiveActions(s, auth.Caller{Principal: "google:carol", Route: "atlas/eng"})
	if !byCarol("mcp:register_group") {
		t.Fatal("a tool the sender explicitly holds must be listed, or its gate never runs")
	}
}
