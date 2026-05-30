package main

import (
	"testing"

	"github.com/kronael/arizuko/chanlib"
)

// TestCapImplConsistency locks mastd's advertised caps against its verb
// implementations. A zero-value *mastoClient suffices: real verbs deref the
// nil mastodon client (or fail JID parsing) → "implemented"; stubs return
// Unsupported up front.
func TestCapImplConsistency(t *testing.T) {
	if drift := chanlib.CapImplReport(&mastoClient{}, caps); len(drift) > 0 {
		for _, d := range drift {
			t.Errorf("cap/impl drift: %s", d)
		}
	}
}
