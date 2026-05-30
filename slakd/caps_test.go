package main

import (
	"testing"

	"github.com/kronael/arizuko/chanlib"
)

// TestCapImplConsistency locks slakd's advertised caps against its verb
// implementations. A zero-value *bot suffices: real verbs fail JID parsing
// (non-Unsupported) → "implemented"; stubs (Forward/Quote/Repost) and the
// absent FetchHistory return Unsupported.
func TestCapImplConsistency(t *testing.T) {
	if drift := chanlib.CapImplReport(&bot{}, caps); len(drift) > 0 {
		for _, d := range drift {
			t.Errorf("cap/impl drift: %s", d)
		}
	}
}
