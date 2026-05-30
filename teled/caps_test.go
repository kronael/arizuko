package main

import (
	"testing"

	"github.com/kronael/arizuko/chanlib"
)

// TestCapImplConsistency locks teled's advertised caps against its verb
// implementations. A zero-value *bot suffices: real verbs deref the nil
// tgbotapi client (panic → "implemented") or fail JID parsing; quote/repost/
// dislike are Unsupported stubs and correctly unadvertised.
func TestCapImplConsistency(t *testing.T) {
	if drift := chanlib.CapImplReport(&bot{}, caps); len(drift) > 0 {
		for _, d := range drift {
			t.Errorf("cap/impl drift: %s", d)
		}
	}
}
