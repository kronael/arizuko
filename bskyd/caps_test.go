package main

import (
	"testing"

	"github.com/kronael/arizuko/chanlib"
)

// TestCapImplConsistency locks bskyd's advertised caps against its verb
// implementations. A zero-value *bskyClient suffices: real verbs hit the nil
// http client / missing session → "implemented"; stubs return Unsupported.
func TestCapImplConsistency(t *testing.T) {
	if drift := chanlib.CapImplReport(&bskyClient{}, caps); len(drift) > 0 {
		for _, d := range drift {
			t.Errorf("cap/impl drift: %s", d)
		}
	}
}
