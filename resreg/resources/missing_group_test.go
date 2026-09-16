package resources

// The missing-group rule (spec 5/8 §"The missing-group rule") against the REAL
// resource declarations and the REAL schema — nothing under the rule binds a
// folder reference to a group, so the rule is the only thing that catches one,
// and a synthetic resource would prove nothing.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore/dynstore"
	"github.com/kronael/arizuko/resreg"
	"github.com/kronael/arizuko/store"
)

// seedGroup lands a groups row through store's own writer — the fixture must
// not go through the engine under test.
func seedGroup(t *testing.T, db *dynstore.Store, folder string) {
	t.Helper()
	s := store.NewOver(db)
	if err := s.PutGroupRow(core.Group{
		Folder: folder, Product: "assistant",
		AddedAt: time.Date(2026, 8, 6, 0, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("seed group %s: %v", folder, err)
	}
	if _, ok := s.GroupByFolder(folder); !ok {
		t.Fatalf("seed group %s did not land", folder)
	}
}

// folderRules counts one folder's egress rules.
func folderRules(t *testing.T, db *dynstore.Store, folder string) int {
	t.Helper()
	rules, err := store.NewOver(db).ListNetworkRules(folder)
	if err != nil {
		t.Fatalf("ListNetworkRules(%q): %v", folder, err)
	}
	return len(rules)
}

// TestMissingGroup_NetworkRulesOrphanIsRealToday proves the gap the rule
// closes is real, not hypothetical: nothing binds a network rule's folder to a
// group, so an apply naming a folder that is not a group commits a silently
// orphaned row. This test asserts the ORPHAN LANDS when the preflight is
// bypassed — if a binding is ever added, this test fails and the rule's
// justification must be re-read, which is exactly the signal wanted.
func TestMissingGroup_NetworkRulesOrphanIsRealToday(t *testing.T) {
	db := openMem(t)
	seedGroup(t, db, "corp/eng")

	// store.SeedRoutdFloor lands two folder='' instance-global rows, so scope
	// the before/after counts to the folder under test rather than the entity.
	if before := folderRules(t, db, "corp/typo"); before != 0 {
		t.Fatalf("corp/typo must start with no rules, got %d", before)
	}

	// No preflight: straight to Apply, naming a folder that is not a group.
	if _, err := resreg.Apply(context.Background(), resreg.On(db), resreg.SubsystemRoutd,
		routdChecksum(t, db), false, map[string]any{
			"network_rules": []NetworkRulesRow{{Folder: "corp/typo", Target: "api.example.com"}},
		}, nil); err != nil {
		t.Fatalf("apply without preflight should succeed today (that IS the gap): %v", err)
	}
	if after := folderRules(t, db, "corp/typo"); after != 1 {
		t.Fatalf("orphan network_rules row count = %d, want 1 — the gap this rule closes", after)
	}
}

// TestMissingGroup_RefusesUnknownFolder: the same manifest, run through the
// rule, is refused — and named precisely.
func TestMissingGroup_RefusesUnknownFolder(t *testing.T) {
	db := openMem(t)
	seedGroup(t, db, "corp/eng")

	manifest := map[string]any{
		"network_rules": []NetworkRulesRow{{Folder: "corp/typo", Target: "api.example.com"}},
	}
	known, err := resreg.KnownFolders(resreg.On(db), manifest)
	if err != nil {
		t.Fatalf("KnownFolders: %v", err)
	}
	if !known["corp/eng"] {
		t.Fatalf("live group corp/eng missing from known set %v", known)
	}
	verr := resreg.ValidateFolderRefs(resreg.SubsystemRoutd, manifest, known)
	if verr == nil {
		t.Fatal("ValidateFolderRefs accepted a rule for a folder that is not a group")
	}
	if !errors.Is(verr, resreg.ErrMissingGroup) {
		t.Errorf("error = %v, want ErrMissingGroup", verr)
	}
	if got := verr.Error(); !strings.Contains(got, "network_rules -> corp/typo") {
		t.Errorf("error %q must name the resource and the folder", got)
	}
}

// TestMissingGroup_AcceptsFolderDeclaredInTheSameManifest: the rule's "or
// declared somewhere in the manifest set" half. A restore onto a fresh
// instance carries the groups row and its rules in one apply; refusing that
// would make the rule refuse every genuine DR restore.
func TestMissingGroup_AcceptsFolderDeclaredInTheSameManifest(t *testing.T) {
	db := openMem(t)
	// Deliberately NO live group: only the manifest declares it.
	manifest := map[string]any{
		"groups":        []GroupsRow{{Folder: "new/team", Product: "assistant"}},
		"network_rules": []NetworkRulesRow{{Folder: "new/team", Target: "api.example.com"}},
	}
	known, err := resreg.KnownFolders(resreg.On(db), manifest)
	if err != nil {
		t.Fatalf("KnownFolders: %v", err)
	}
	if err := resreg.ValidateFolderRefs(resreg.SubsystemRoutd, manifest, known); err != nil {
		t.Fatalf("a folder the manifest itself declares must pass: %v", err)
	}
	// And it really applies: the rule must not be a check that only ever says no.
	if _, err := resreg.Apply(context.Background(), resreg.On(db), resreg.SubsystemRoutd,
		routdChecksum(t, db), false, manifest, nil); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if n := folderRules(t, db, "new/team"); n != 1 {
		t.Fatalf("network_rules rows for new/team = %d, want 1", n)
	}
}

// TestMissingGroup_InstanceGlobalFolderExempt: network_rules carries
// folder=” for instance-global egress rules — the very reason the folder is
// bound to nothing. The rule must not reject them.
func TestMissingGroup_InstanceGlobalFolderExempt(t *testing.T) {
	db := openMem(t)
	manifest := map[string]any{
		"network_rules": []NetworkRulesRow{{Folder: "", Target: "api.anthropic.com"}},
	}
	known, err := resreg.KnownFolders(resreg.On(db), manifest)
	if err != nil {
		t.Fatalf("KnownFolders: %v", err)
	}
	if err := resreg.ValidateFolderRefs(resreg.SubsystemRoutd, manifest, known); err != nil {
		t.Fatalf("instance-global folder='' must be exempt: %v", err)
	}
}

// TestMissingGroup_CatchesWebRoutesFolder: web_routes names a folder the same
// way network_rules does, and this is the second resource the rule reads — so
// a rule that only ever looked at one of them would pass its own tests. It
// refuses before any transaction opens, and names the resource and the folder.
func TestMissingGroup_CatchesWebRoutesFolder(t *testing.T) {
	db := openMem(t)
	seedGroup(t, db, "corp/eng")
	manifest := map[string]any{
		"web_routes": []WebRoutesRow{{PathPrefix: "/x/", Access: "public", Folder: "corp/ghost"}},
	}
	known, err := resreg.KnownFolders(resreg.On(db), manifest)
	if err != nil {
		t.Fatalf("KnownFolders: %v", err)
	}
	verr := resreg.ValidateFolderRefs(resreg.SubsystemRoutd, manifest, known)
	if !errors.Is(verr, resreg.ErrMissingGroup) {
		t.Fatalf("error = %v, want ErrMissingGroup", verr)
	}
	if !strings.Contains(verr.Error(), "web_routes -> corp/ghost") {
		t.Errorf("error %q must name web_routes and the folder", verr.Error())
	}
}

// TestMissingGroup_CleanManifestPasses guards against a rule that refuses
// everything: the ordinary case — every reference resolvable — must produce
// no error at all.
func TestMissingGroup_CleanManifestPasses(t *testing.T) {
	db := openMem(t)
	seedGroup(t, db, "corp/eng")
	manifest := map[string]any{
		"network_rules": []NetworkRulesRow{{Folder: "corp/eng", Target: "api.example.com"}},
		"web_routes":    []WebRoutesRow{{PathPrefix: "/eng/", Access: "public", Folder: "corp/eng"}},
	}
	known, err := resreg.KnownFolders(resreg.On(db), manifest)
	if err != nil {
		t.Fatalf("KnownFolders: %v", err)
	}
	if err := resreg.ValidateFolderRefs(resreg.SubsystemRoutd, manifest, known); err != nil {
		t.Fatalf("clean manifest must pass: %v", err)
	}
}
