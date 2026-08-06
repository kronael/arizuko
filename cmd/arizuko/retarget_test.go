package main

// `apply --as-folder` (spec 5/8 §"Path retargeting"): the recipe that wires
// resreg.Resource.Retarget to a real caller. Drives retargetDocs + applyDocs,
// the two functions cmdApply composes, because cmdApply itself os.Exits.

import (
	"context"
	"strings"
	"testing"

	"github.com/kronael/arizuko/resreg"
	"github.com/kronael/arizuko/resreg/resources"
)

// oneFolderDocs is a single-folder routd manifest: the shape --as-folder
// accepts. redirect_to deliberately embeds the folder, which is the gap
// Retarget itself does not close.
func oneFolderDocs(folder string) []parsedDoc {
	return []parsedDoc{{
		subsystem: resreg.SubsystemRoutd,
		manifest: map[string]any{
			"groups":        []resources.GroupsRow{{Folder: folder, Product: "assistant"}},
			"network_rules": []resources.NetworkRulesRow{{Folder: folder, Target: "api.example.com"}},
			"web_routes": []resources.WebRoutesRow{
				{PathPrefix: "/site/", Access: "public", Folder: folder, RedirectTo: "/pub/" + folder + "/index.html"},
			},
		},
	}}
}

// TestRetarget_RewritesScopeAndRedirect: every scoped row moves to the new
// folder, AND web_routes.redirect_to follows — a retargeted route that still
// pointed at the source folder's web root would serve the wrong files.
func TestRetarget_RewritesScopeAndRedirect(t *testing.T) {
	docs := oneFolderDocs("corp/eng")
	src, err := retargetDocs(docs, "corp/newteam")
	if err != nil {
		t.Fatalf("retargetDocs: %v", err)
	}
	if src != "corp/eng" {
		t.Errorf("source folder = %q, want corp/eng", src)
	}

	groups := docs[0].manifest["groups"].([]resources.GroupsRow)
	if len(groups) != 1 || groups[0].Folder != "corp/newteam" {
		t.Errorf("groups not retargeted: %+v", groups)
	}
	rules := docs[0].manifest["network_rules"].([]resources.NetworkRulesRow)
	if len(rules) != 1 || rules[0].Folder != "corp/newteam" {
		t.Errorf("network_rules not retargeted: %+v", rules)
	}
	routes := docs[0].manifest["web_routes"].([]resources.WebRoutesRow)
	if len(routes) != 1 {
		t.Fatalf("web_routes lost rows: %+v", routes)
	}
	if routes[0].Folder != "corp/newteam" {
		t.Errorf("web_routes.folder = %q, want corp/newteam", routes[0].Folder)
	}
	if routes[0].RedirectTo != "/pub/corp/newteam/index.html" {
		t.Errorf("web_routes.redirect_to = %q, still points at the source folder's web root", routes[0].RedirectTo)
	}
	// Nothing may still name the source.
	if resreg.ManifestScopes(resreg.SubsystemRoutd, docs[0].manifest)[0] == "corp/eng" {
		t.Error("a row still names the source folder")
	}
}

// TestRetarget_RefusesMultiFolderManifest: retargeting several folders onto
// one would merge them silently. It must refuse, not guess.
func TestRetarget_RefusesMultiFolderManifest(t *testing.T) {
	docs := []parsedDoc{{
		subsystem: resreg.SubsystemRoutd,
		manifest: map[string]any{
			"groups": []resources.GroupsRow{
				{Folder: "corp/eng", Product: "assistant"},
				{Folder: "corp/sales", Product: "assistant"},
			},
		},
	}}
	_, err := retargetDocs(docs, "corp/newteam")
	if err == nil {
		t.Fatal("a two-folder manifest must be refused")
	}
	if !containsAll(err.Error(), "corp/eng", "corp/sales") {
		t.Errorf("error %q must name both folders it found", err.Error())
	}
	// And it must not have half-rewritten anything on its way out.
	groups := docs[0].manifest["groups"].([]resources.GroupsRow)
	if groups[0].Folder != "corp/eng" || groups[1].Folder != "corp/sales" {
		t.Errorf("a refused retarget mutated the manifest: %+v", groups)
	}
}

// TestRetarget_RefusesInstanceGlobalRows: network_rules' folder='' rows are
// the instance-wide egress allowlist. Retargeting them would narrow an
// instance-wide allowlist to one folder — a privilege change, not a rename —
// so a manifest mixing them with a folder's rows is refused.
func TestRetarget_RefusesInstanceGlobalRows(t *testing.T) {
	docs := []parsedDoc{{
		subsystem: resreg.SubsystemRoutd,
		manifest: map[string]any{
			"network_rules": []resources.NetworkRulesRow{
				{Folder: "corp/eng", Target: "api.example.com"},
				{Folder: "", Target: "api.anthropic.com"},
			},
		},
	}}
	if _, err := retargetDocs(docs, "corp/newteam"); err == nil {
		t.Fatal("a manifest carrying instance-global rows must be refused")
	}
	rules := docs[0].manifest["network_rules"].([]resources.NetworkRulesRow)
	if rules[1].Folder != "" {
		t.Errorf("the instance-global row was retargeted to %q", rules[1].Folder)
	}
}

// TestRetarget_AppliesUnderTheNewFolder is the end-to-end half: a retargeted
// manifest really lands in routd.db under the new folder and leaves no row
// under the old one. Without it every assertion above could hold for a
// retarget that produced an unapplyable manifest.
func TestRetarget_AppliesUnderTheNewFolder(t *testing.T) {
	_, stores := openInstance(t)
	db := stores[resreg.SubsystemRoutd].DB()

	docs := oneFolderDocs("corp/eng")
	if _, err := retargetDocs(docs, "corp/newteam"); err != nil {
		t.Fatalf("retargetDocs: %v", err)
	}
	if bad := preflightFolders(stores, docs); len(bad) > 0 {
		t.Fatalf("the retargeted manifest declares its own group, so the preflight must pass: %v", bad)
	}
	// force: a retargeted manifest's checksum can never match, by construction.
	if _, err := applyDocs(context.Background(), stores, docs, true, "test", ""); err != nil {
		t.Fatalf("applyDocs: %v", err)
	}

	if got := scalar(t, db, `SELECT COUNT(*) FROM groups WHERE folder='corp/newteam'`); got != 1 {
		t.Errorf("group not created under the new folder (count=%d)", got)
	}
	if got := scalar(t, db, `SELECT COUNT(*) FROM groups WHERE folder='corp/eng'`); got != 0 {
		t.Errorf("the source folder was created too (count=%d)", got)
	}
	if got := scalar(t, db, `SELECT COUNT(*) FROM network_rules WHERE folder='corp/newteam'`); got != 1 {
		t.Errorf("egress rule not under the new folder (count=%d)", got)
	}
	var redirect string
	if err := db.QueryRow(`SELECT redirect_to FROM web_routes WHERE folder='corp/newteam'`).Scan(&redirect); err != nil {
		t.Fatalf("web_route not applied: %v", err)
	}
	if strings.Contains(redirect, "corp/eng") {
		t.Errorf("redirect_to %q still points at the source folder's web root", redirect)
	}
}
