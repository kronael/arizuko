package main

import (
	"testing"

	"github.com/kronael/arizuko/chanlib"
)

// TestCapImplConsistency locks linkd's advertised caps against its verb
// implementations. A zero-value *linkClient suffices: real verbs reject the
// empty URN with a non-Unsupported error → "implemented";
// Forward/Quote/Dislike/Edit and SendFile return Unsupported.
func TestCapImplConsistency(t *testing.T) {
	if drift := chanlib.CapImplReport(&linkClient{}, caps); len(drift) > 0 {
		for _, d := range drift {
			t.Errorf("cap/impl drift: %s", d)
		}
	}
}
