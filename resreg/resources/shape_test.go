package resources

// Apply and ExportSnapshot run over ITEMS. Every registered resource that names
// an owner subsystem has to be entity-shaped for that to hold, and the check is
// here rather than in resreg because the registrations are here: resreg's own
// tests can only build the fixtures they register themselves.

import (
	"testing"

	"github.com/kronael/arizuko/resreg"
)

func TestEveryOwnedResourceIsEntityShaped(t *testing.T) {
	owned := 0
	for _, r := range resreg.All() {
		if r.DB == "" {
			// A resource with no owner subsystem is catalog-only: it drives
			// /openapi.json and the CLI manifest, and no apply reaches it —
			// SubsystemOf can return "" and the CLI then has no store for it.
			continue
		}
		owned++
		if r.DB != resreg.SubsystemRoutd && r.DB != resreg.SubsystemOnbod {
			t.Errorf("%s names subsystem %q, which no owner database answers to",
				r.Name, r.DB)
		}
		if !r.IsEntity() {
			t.Errorf("%s is owned by %s and declares no EntitySortKey. Apply and "+
				"ExportSnapshot read and write items; a column-shaped resource has "+
				"nothing to run them through", r.Name, r.DB)
		}
	}
	if owned == 0 {
		t.Fatal("no owned resource is registered, so this test proves nothing — " +
			"the resources package did not init")
	}
	t.Logf("%d owned resources, all entity-shaped", owned)
}
