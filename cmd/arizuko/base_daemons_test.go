package main

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/kronael/arizuko/compose"
)

// baseDaemons is a SECOND declaration of which services compose always emits —
// compose writes each one by its own call rather than from a list, so there is
// no list to import (BUGS L200 names this the sixth copy and defers collapsing
// it with the rest).
//
// A copy that nobody checks drifts, and this one drifts QUIETLY: `packages add`
// warns "unsatisfied dependency" for a depends_on outside the set, so a core
// daemon missing here makes the CLI warn about a dependency that is in fact
// always present, and an operator learns to ignore the warning.
//
// So: every name in baseDaemons must be a service compose actually emits.
func TestBaseDaemonsAreServicesComposeEmits(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "services"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".env"),
		[]byte("APP=arizuko\nFLAVOR=test\nONBOARDING_ENABLED=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, _, err := compose.Generate(dir)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	// Service stanzas are the two-space keys under `services:`.
	var emitted []string
	for _, m := range regexp.MustCompile(`(?m)^  ([a-z0-9_-]+):$`).FindAllStringSubmatch(out, -1) {
		emitted = append(emitted, m[1])
	}
	if len(emitted) == 0 {
		t.Fatal("no service stanza parsed; this test is reading the wrong shape")
	}

	var missing []string
	for name := range baseDaemons {
		if !slices.Contains(emitted, name) {
			missing = append(missing, name)
		}
	}
	slices.Sort(missing)
	if len(missing) > 0 {
		t.Errorf("baseDaemons names %v, which compose does not emit — `packages add` "+
			"would call a satisfied depends_on unsatisfied. Emitted: %s",
			missing, strings.Join(emitted, ", "))
	}
}
