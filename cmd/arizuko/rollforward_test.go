package main

// The operator's half of BUGS L267: an `arizuko apply` that dies part-way holds
// its subsystem's claim, and the escape must be a command rather than an edit of
// `resreg_scopes`.
//
// THE FAILURE IS REAL, not simulated. The wedging document declares two
// `network_rules` rows at one coordinate, which the object path refuses when it
// renders them — after `groups`, earlier in the catalog, has already written. So
// routd ends part-applied with its claim held, exactly as an interrupted apply
// leaves it. Nothing here stubs resreg.Apply and nothing writes the marker by
// hand.

import (
	"context"
	"errors"
	"testing"

	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/resreg"
	"github.com/kronael/arizuko/resreg/resources"
	"github.com/kronael/arizuko/store"
)

// wedgeRoutd runs an apply that writes and then fails, and answers the
// subsystem's claim so a caller can prove the fixture reproduced the state.
func wedgeRoutd(t *testing.T, stores map[string]*store.Store) string {
	t.Helper()
	routdSum, err := resreg.Checksum(resreg.On(stores[resreg.SubsystemRoutd].Obj()), resreg.SubsystemRoutd)
	if err != nil {
		t.Fatal(err)
	}
	docs := []parsedDoc{{
		subsystem: resreg.SubsystemRoutd,
		checksum:  routdSum,
		manifest: map[string]any{
			"groups": []resources.GroupsRow{
				{Folder: "corp/eng", Product: "assistant"},
				{Folder: "lost/work", Product: "assistant"},
			},
			// Two rows at one (folder, target): refused where the puts are
			// rendered, and by then `groups` has committed.
			"network_rules": []resources.NetworkRulesRow{
				{Folder: "lost/work", Target: "dup.example.com"},
				{Folder: "lost/work", Target: "dup.example.com"},
			},
		},
	}}
	if _, err := applyDocs(context.Background(), stores, docs, applyRun{actor: "test"}); err == nil {
		t.Fatal("the wedging apply succeeded, so nothing here is part-applied")
	}
	holder := scopeHolder(t, stores)
	if holder == "" {
		t.Fatal("the failed apply left no claim, so the fixture never reproduced the state " +
			"this file is about")
	}
	return holder
}

// scopeHolder reads routd's version marker straight through the driver, so an
// assertion about the claim cannot agree with a bug in the protocol that sets it.
func scopeHolder(t *testing.T, stores map[string]*store.Store) string {
	t.Helper()
	sc := store.OwnerScope(resreg.SubsystemRoutd)
	items, err := stores[resreg.SubsystemRoutd].Obj().Query(t.Context(), objstore.Query{
		Entity: sc.Entity, Partition: sc.Key.Partition,
		Key: objstore.KeyPredicate{Op: objstore.ExactKey, Value: sc.Key.Sort},
	})
	if err != nil {
		t.Fatalf("read routd's marker: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("routd carries %d markers, want 1", len(items))
	}
	return items[0].Attrs["holder"]
}

// operatorDoc is the config the operator wants applied, which is NOT the work the
// wedged claim was doing.
func operatorDoc(t *testing.T, stores map[string]*store.Store) []parsedDoc {
	t.Helper()
	return []parsedDoc{{
		subsystem: resreg.SubsystemRoutd,
		checksum:  "sha256:stale", // the part-written scope hashes to neither state
		manifest: map[string]any{
			"groups": []resources.GroupsRow{{Folder: "corp/eng", Product: "assistant"}},
			"network_rules": []resources.NetworkRulesRow{
				{Folder: "corp/eng", Target: "wanted.example.com"},
			},
		},
	}}
}

// A held scope must not stop the run at the PRE-IMAGE read, because the pre-image
// error names nothing the operator can act on. The apply is what knows whether
// this manifest may finish the claim, so its refusal is the one that reaches them.
func TestRollForward_AHeldScopeRefusesAtTheApplyRatherThanThePreImage(t *testing.T) {
	_, stores := openInstance(t)
	seedRoutdConfig(t, stores[resreg.SubsystemRoutd])
	held := wedgeRoutd(t, stores)

	_, err := applyDocs(context.Background(), stores, operatorDoc(t, stores), applyRun{actor: "test"})
	if !errors.Is(err, objstore.ErrScopeHeld) {
		t.Fatalf("applyDocs err = %v, want ErrScopeHeld", err)
	}
	if !containsAll(err.Error(), "apply routd") {
		t.Errorf("error %q must come from the apply, not the pre-image read: the pre-image "+
			"cannot say which manifests may roll the scope forward", err)
	}
	if now := scopeHolder(t, stores); now != held {
		t.Errorf("the refused apply moved the claim %q -> %q", held, now)
	}
	if got := ruleCount(t, stores[resreg.SubsystemRoutd], "corp/eng", "wanted.example.com"); got != 0 {
		t.Errorf("the refused apply wrote %d of its own rules into a scope it does not hold", got)
	}
}

// --take-over is the command L267 asked for: the operator asserts that the
// interrupted work is not coming back, and this document becomes what the scope
// holds. Nothing else clears a claim without finishing work.
func TestRollForward_TakeOverFinishesAScopeTheOperatorCannotReproduce(t *testing.T) {
	_, stores := openInstance(t)
	routdDB := stores[resreg.SubsystemRoutd]
	seedRoutdConfig(t, routdDB)
	wedgeRoutd(t, stores)

	if _, err := applyDocs(context.Background(), stores, operatorDoc(t, stores),
		applyRun{takeOver: true, actor: "test"}); err != nil {
		t.Fatalf("--take-over could not finish the claim: %v", err)
	}
	if h := scopeHolder(t, stores); h != "" {
		t.Errorf("the takeover left the claim %q on the scope", h)
	}
	if got := ruleCount(t, routdDB, "corp/eng", "wanted.example.com"); got != 1 {
		t.Errorf("corp/eng's rule count = %d, want 1", got)
	}
	if got := ruleCount(t, routdDB, "lost/work", ""); got != 0 {
		t.Errorf("the interrupted apply's rules survived (%d) inside this document's own "+
			"delete set", got)
	}

	// And the recovered subsystem is a FIXED POINT: exporting it and applying
	// that back changes nothing and needs no flag. The export is what the
	// operator re-applies — a hand-written document carries no server-owned
	// stamp, so re-applying one moves the checksum whatever the scope's history
	// (BUGS F49).
	settled, err := resreg.Checksum(resreg.On(routdDB.Obj()), resreg.SubsystemRoutd)
	if err != nil {
		t.Fatal(err)
	}
	exported, err := resreg.Export(resreg.On(routdDB.Obj()), resreg.SubsystemRoutd)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := applyDocs(context.Background(), stores, []parsedDoc{{
		subsystem: resreg.SubsystemRoutd, checksum: settled, manifest: exported,
	}}, applyRun{actor: "test"}); err != nil {
		t.Fatalf("the apply after the takeover: %v", err)
	}
	again, err := resreg.Checksum(resreg.On(routdDB.Obj()), resreg.SubsystemRoutd)
	if err != nil {
		t.Fatal(err)
	}
	if again != settled {
		t.Errorf("re-applying the recovered subsystem's own export moved the checksum %s -> %s",
			settled, again)
	}
}
