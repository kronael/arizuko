package main

import (
	"testing"

	"github.com/kronael/arizuko/chanlib"
)

// TestCapImplConsistency locks reditd's advertised caps against its verb
// implementations. A zero-value *redditClient suffices: real verbs return a
// non-Unsupported error on empty input → "implemented"; Forward/Quote/Repost
// and SendFile return Unsupported.
func TestCapImplConsistency(t *testing.T) {
	if drift := chanlib.CapImplReport(&redditClient{}, caps); len(drift) > 0 {
		for _, d := range drift {
			t.Errorf("cap/impl drift: %s", d)
		}
	}
}
