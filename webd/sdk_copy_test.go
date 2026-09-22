package main

import (
	"crypto/sha256"
	"os"
	"testing"
)

// The browser SDK ships twice: webd serves one copy to a chat widget, and the
// operator site documents the other. They diverged silently — the served copy
// cited a spec that does not exist — and nothing said so (BUGS L235).
//
// One renderer, two sinks: the bytes must be identical, and this is what says
// so. A change to either copy fails here until both carry it.
func TestBrowserSDKCopiesAreIdentical(t *testing.T) {
	const served = "assets/arizuko-client.js"
	const documented = "../template/web/pub/arizuko/assets/arizuko-client.js"
	a, err := os.ReadFile(served)
	if err != nil {
		t.Fatalf("read %s: %v", served, err)
	}
	b, err := os.ReadFile(documented)
	if err != nil {
		t.Fatalf("read %s: %v", documented, err)
	}
	if sha256.Sum256(a) != sha256.Sum256(b) {
		t.Errorf("%s and %s have diverged (%d vs %d bytes); copy one onto the other",
			served, documented, len(a), len(b))
	}
}
