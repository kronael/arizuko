package main

import (
	"testing"

	"github.com/kronael/arizuko/chanlib"
)

// TestCapImplConsistency locks emaid's advertised caps against its verb
// implementations. A zero-value *server suffices: FetchHistory derefs the nil
// db (panic → "implemented"); every other gated verb is an Unsupported stub
// (email is immutable / not a feed) and correctly unadvertised.
func TestCapImplConsistency(t *testing.T) {
	if drift := chanlib.CapImplReport(&server{}, caps); len(drift) > 0 {
		for _, d := range drift {
			t.Errorf("cap/impl drift: %s", d)
		}
	}
}
