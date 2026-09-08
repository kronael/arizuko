package resources

// The scope half of a registration: a resource whose rows are ITEMS is read by
// `archive export` through objstore.ReadConsistent, which sees only the writers
// that moved the scope's version marker. store declares which entities move one
// and its writers carry it (store/scopes.go); this is where the two sets are
// compared, because this is where the registrations are known.

import (
	"testing"

	"github.com/kronael/arizuko/resreg"
	"github.com/kronael/arizuko/store"
)

func TestEveryEntityResourceCarriesAScope(t *testing.T) {
	entities := 0
	for _, r := range resreg.All() {
		if !r.IsEntity() {
			continue
		}
		entities++
		got, ok := store.EntityScope(r.Table)
		if !ok {
			t.Errorf("%s is entity-backed and %q moves no version marker; `archive export` "+
				"reads it through objstore.ReadConsistent, so a write that lands under an "+
				"export is invisible and the export calls itself consistent (BUGS L264). "+
				"Declare it in store/scopes.go", r.Name, r.Table)
			continue
		}
		if want := store.OwnerScope(r.DB); got != want {
			t.Errorf("%s writes move %s/%s but the resource is owned by %s; an export of %s "+
				"compares a marker nothing moves", r.Name, got.Key.Partition, got.Key.Sort,
				r.DB, r.DB)
		}
	}
	if entities == 0 {
		t.Fatal("no entity-backed resource was checked, so this test asserts nothing")
	}
}
