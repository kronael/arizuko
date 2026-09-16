package main

// The CLI half of the missing-group rule (spec 5/8 §"The missing-group
// rule"): preflightFolders runs across EVERY parsed document against the
// REAL routd.db/onbod.db pair, before cmdApply opens a transaction.
//
// cmdApply itself calls os.Exit on refusal, which is unsafe from inside a
// test process — so these drive parseDocs + preflightFolders, the two
// functions cmdApply composes, and assert the DB is untouched.

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kronael/arizuko/resreg"
	"github.com/kronael/arizuko/store"
)

// countNetworkRules counts one folder's egress rules in the store the body was
// handed. It reads THAT store rather than reopening the instance's: the fixture
// hands each body a namespace of its own, and a second door would answer for a
// different set of tables.
func countNetworkRules(t *testing.T, stores map[string]*store.Store, folder string) int {
	t.Helper()
	return ruleCount(t, stores[resreg.SubsystemRoutd], folder, "")
}

// twoDocManifest is a routd document plus an onbod document, the shape
// `arizuko export` emits — so the preflight is exercised on a document SET,
// which is the only way its cross-document half can be wrong.
const twoDocManifest = `groups:
    - folder: corp/eng
      product: assistant
network_rules:
    - folder: %s
      target: api.example.com
---
onboarding_gates:
    - gate: invite_required
      limit_per_day: 0
      enabled: 0
`

// TestPreflightFolders_RefusesUnknownFolderAcrossDocuments: a network_rules
// row naming a folder no groups row declares is refused, and — the part that
// matters — routd.db still holds nothing for it afterwards.
func TestPreflightFolders_RefusesUnknownFolderAcrossDocuments(t *testing.T) {
	dataDir, stores := openInstance(t)

	if got := countNetworkRules(t, stores, "corp/ghost"); got != 0 {
		t.Fatalf("corp/ghost rules before = %d, want 0", got)
	}

	yaml := strings.Replace(twoDocManifest, "%s", "corp/ghost", 1)
	docs, perr := parseDocs(filepath.Join(dataDir, "m.yaml"), []byte(yaml))
	if perr != nil {
		t.Fatalf("parseDocs: %v", perr)
	}
	if len(docs) != 2 {
		t.Fatalf("parsed %d documents, want 2 — the cross-document case is what this tests", len(docs))
	}
	bad := preflightFolders(stores, docs)
	if len(bad) != 1 {
		t.Fatalf("preflight returned %d errors, want 1: %v", len(bad), bad)
	}
	if !errors.Is(bad[0], resreg.ErrMissingGroup) {
		t.Errorf("error = %v, want ErrMissingGroup", bad[0])
	}
	if !strings.Contains(bad[0].Error(), "corp/ghost") {
		t.Errorf("error %q must name the offending folder", bad[0].Error())
	}
	if got := countNetworkRules(t, stores, "corp/ghost"); got != 0 {
		t.Errorf("corp/ghost rules after a refused preflight = %d, want 0", got)
	}
}

// TestPreflightFolders_AcceptsFolderDeclaredInAnotherDocument: the same
// manifest with the folder its own groups row declares passes, and applies.
// Without this the test above would pass for a preflight that refuses
// everything.
func TestPreflightFolders_AcceptsFolderDeclaredInAnotherDocument(t *testing.T) {
	dataDir, stores := openInstance(t)

	yaml := strings.Replace(twoDocManifest, "%s", "corp/eng", 1)
	docs, perr := parseDocs(filepath.Join(dataDir, "m.yaml"), []byte(yaml))
	if perr != nil {
		t.Fatalf("parseDocs: %v", perr)
	}
	if len(docs) != 2 {
		t.Fatalf("parsed %d documents, want 2", len(docs))
	}
	if bad := preflightFolders(stores, docs); len(bad) > 0 {
		t.Fatalf("clean manifest refused: %v", bad)
	}
}

// TestPreflightFolders_ExportRoundTripPasses is the regression this rule is
// most likely to break: store.SeedRoutdFloor writes two folder=” instance-global
// network_rules into every routd.db born empty, so a plain export of a FRESH instance
// carries them. If the rule did not exempt the empty folder, `arizuko export
// | arizuko apply` would refuse on a stock instance.
func TestPreflightFolders_ExportRoundTripPasses(t *testing.T) {
	dataDir, stores := openInstance(t)

	if globals := countNetworkRules(t, stores, ""); globals == 0 {
		t.Fatal("fixture is vacuous: a fresh routd.db must carry the seeded folder='' rules")
	}

	manifest, err := resreg.Export(resreg.On(stores[resreg.SubsystemRoutd].Obj()), resreg.SubsystemRoutd)
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	out, err := resreg.EmitYAML(manifest)
	if err != nil {
		t.Fatalf("emit: %v", err)
	}
	docs, perr := parseDocs(filepath.Join(dataDir, "export.yaml"), out)
	if perr != nil {
		t.Fatalf("parseDocs: %v", perr)
	}
	if bad := preflightFolders(stores, docs); len(bad) > 0 {
		t.Fatalf("a fresh instance's own export must round-trip: %v", bad)
	}
}
