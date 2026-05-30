package main

import (
	"testing"

	"github.com/kronael/arizuko/chanlib"
)

// TestCapImplConsistency locks discd's advertised capability map against its
// actual verb implementations: no advertised gated cap may map to an
// Unsupported stub, and no real verb may be left unadvertised. A zero-value
// *bot is enough — real impls deref the nil discordgo session and panic
// (detected as "implemented"); stubs return Unsupported up front.
func TestCapImplConsistency(t *testing.T) {
	if drift := chanlib.CapImplReport(&bot{}, caps); len(drift) > 0 {
		for _, d := range drift {
			t.Errorf("cap/impl drift: %s", d)
		}
	}
}
