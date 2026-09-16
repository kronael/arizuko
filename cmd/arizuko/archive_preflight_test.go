package main

// `archive apply`'s config phase (spec 5/8 §"Consistency levels", the
// pre-flight bullet: "before any subsystem's transaction opens, apply checks
// referential integrity across the archive's documents ... and refuses the
// whole restore rather than importing a half-wired instance").
//
// The archive's config documents are buffered, not applied as they stream
// past, precisely so the check can see the whole set. These tests assert the
// refusal AND that routd.db is untouched afterwards.

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/kronael/arizuko/resreg"
)

// TestArchiveApply_RefusesFolderThatIsNotAGroup: an archive whose routd
// document carries an egress rule for a folder no groups row declares is
// refused whole, before anything is written.
func TestArchiveApply_RefusesFolderThatIsNotAGroup(t *testing.T) {
	ctx := context.Background()
	srcDataDir, srcStores := openInstance(t)
	srcDB := srcStores[resreg.SubsystemRoutd]
	// A rule for a folder with no group. The writer accepts it, which is the
	// whole point: nothing binds network_rules.folder to a group, so the refusal
	// below has to come from the preflight rather than from storage.
	if err := srcDB.AddNetworkRule("corp/ghost", "api.example.com", "seed"); err != nil {
		t.Fatal(err)
	}
	if got := ruleCount(t, srcDB, "corp/ghost", ""); got != 1 {
		t.Fatalf("source fixture did not land (count=%d)", got)
	}

	var buf bytes.Buffer
	if _, err := buildArchive(ctx, srcStores, srcDataDir, false, &buf); err != nil {
		t.Fatal(err)
	}

	dstDataDir, dstStores := openInstance(t)
	dstDB := dstStores[resreg.SubsystemRoutd]
	beforeGroups := groupCount(t, dstDB, "")

	// --force is on deliberately: it overrides the CAS, and must NOT override
	// the missing-group rule.
	_, err := applyArchive(ctx, dstStores, dstDataDir, bytes.NewReader(buf.Bytes()), true, nil)
	if err == nil {
		t.Fatal("archive apply must refuse an archive referencing a folder that is not a group")
	}
	if !strings.Contains(err.Error(), "corp/ghost") {
		t.Errorf("error %q must name the offending folder", err.Error())
	}

	if got := ruleCount(t, dstDB, "corp/ghost", ""); got != 0 {
		t.Errorf("the orphan rule was imported anyway (count=%d)", got)
	}
	if got := groupCount(t, dstDB, ""); got != beforeGroups {
		t.Errorf("groups moved on a refused restore: %d -> %d", beforeGroups, got)
	}
}

// TestArchiveApply_CleanArchiveStillApplies is the other direction: the same
// pipeline, with every reference resolvable, must still restore. Without it
// the test above would pass for a config phase that refuses everything —
// or never runs at all.
func TestArchiveApply_CleanArchiveStillApplies(t *testing.T) {
	ctx := context.Background()
	srcDataDir, srcStores := openInstance(t)
	srcDB := srcStores[resreg.SubsystemRoutd]
	seedRoutdConfig(t, srcDB)

	var buf bytes.Buffer
	if _, err := buildArchive(ctx, srcStores, srcDataDir, false, &buf); err != nil {
		t.Fatal(err)
	}

	dstDataDir, dstStores := openInstance(t)
	dstDB := dstStores[resreg.SubsystemRoutd]
	if got := groupCount(t, dstDB, "corp/eng"); got != 0 {
		t.Fatalf("destination is not fresh (count=%d)", got)
	}

	report, err := applyArchive(ctx, dstStores, dstDataDir, bytes.NewReader(buf.Bytes()), true, nil)
	if err != nil {
		t.Fatalf("applyArchive: %v\n%s", err, report)
	}
	if got := groupCount(t, dstDB, "corp/eng"); got != 1 {
		t.Errorf("group not restored (count=%d)", got)
	}
	if got := ruleCount(t, dstDB, "corp/eng", "original.example.com"); got != 1 {
		t.Errorf("egress rule not restored (count=%d)", got)
	}
	if !strings.Contains(report, "routd: applied") {
		t.Errorf("report must record the config phase: %s", report)
	}
}
