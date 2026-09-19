package main

import (
	"io"
	"os"
	"strings"
	"testing"

	"github.com/kronael/arizuko/resreg"
	_ "github.com/kronael/arizuko/resreg/resources" // registers the catalog resreg.All() walks
)

// capturePlan runs printPlan with stdout redirected and returns what it wrote.
func capturePlan(t *testing.T, deltas []resreg.ResourceDelta) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	saved := os.Stdout
	os.Stdout = w
	printPlan(deltas)
	w.Close()
	os.Stdout = saved
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read captured stdout: %v", err)
	}
	return string(out)
}

// BUGS R1-5: printPlan hard-coded "set via `arizuko secret set`" on the
// SkipApplyRebuild branch, which is advice only ONE of the flagged resources
// can act on. The flag is not a secrets marker — every resource carrying it is
// managed by a different verb (`arizuko secret set`, route-token mint, invite
// create, the onboarding flow, `arizuko packages`), so the line told an
// operator planning invites to run a secrets command.
//
// This asserts the property rather than the string: the informational line must
// be identical for every SkipApplyRebuild resource except for the resource's own
// name. Re-introducing per-resource advice fails it.
func TestPrintPlan_SkipApplyRebuildAdviceIsResourceAgnostic(t *testing.T) {
	var skipped []string
	for _, r := range resreg.All() {
		if r.SkipApplyRebuild {
			skipped = append(skipped, r.Name)
		}
	}
	// The premise of the bug: more than one resource carries the flag, so no
	// single command can be correct for all of them.
	if len(skipped) < 2 {
		t.Fatalf("SkipApplyRebuild resources = %v; want at least 2 (the whole point of R1-5)", skipped)
	}

	var shapes []string
	for _, name := range skipped {
		line := strings.TrimSpace(capturePlan(t, []resreg.ResourceDelta{{
			Resource:         name,
			SkipApplyRebuild: true,
			Live:             1, // the line reports rows held, so one row makes it print
		}}))
		if !strings.HasPrefix(line, name+":") {
			t.Errorf("%s: line %q must name its own resource", name, line)
		}
		shapes = append(shapes, strings.TrimPrefix(line, name+":"))
	}
	for i, s := range shapes[1:] {
		if s != shapes[0] {
			t.Errorf("advice differs per resource: %s => %q, %s => %q",
				skipped[0], shapes[0], skipped[i+1], s)
		}
	}
	if strings.Contains(shapes[0], "arizuko secret set") {
		t.Errorf("advice names a secrets-only command for every skipped resource: %q", shapes[0])
	}
	if !strings.Contains(shapes[0], "not applied") {
		t.Errorf("advice must still say the rows are not applied: %q", shapes[0])
	}
}

// A SkipApplyRebuild resource with no rows prints nothing: the line is
// informational, so an empty table must not make `plan` look like it has
// something to report.
func TestPrintPlan_SkipApplyRebuildEmptyPrintsNothing(t *testing.T) {
	out := capturePlan(t, []resreg.ResourceDelta{{
		Resource:         "secrets",
		SkipApplyRebuild: true,
	}})
	if strings.Contains(out, "secrets") {
		t.Errorf("empty skipped resource printed %q, want no mention", out)
	}
}

// The count is the LIVE row count, not the number of distinct PKs the manifest
// carried. route_tokens and invites keep their runtime-minted digest out of
// YAML, so every row of theirs parses back to the same identity — a line adding
// the PK lists up says one where the instance holds 43 (BUGS L260).
func TestPrintPlan_SkipApplyRebuildCountsLiveRows(t *testing.T) {
	out := capturePlan(t, []resreg.ResourceDelta{{
		Resource:         "route_tokens",
		SkipApplyRebuild: true,
		Live:             43,
		// What a plan against a full export yields: 43 rows, one identity.
		Add: []string{"|"},
	}})
	if !strings.Contains(out, "route_tokens: 43 set") {
		t.Errorf("plan line = %q, want the 43 live tokens named", strings.TrimSpace(out))
	}
}
