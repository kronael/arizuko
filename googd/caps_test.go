package main

import (
	"testing"

	"github.com/kronael/arizuko/chanlib"
)

func TestCapImplConsistency(t *testing.T) {
	for _, credentialed := range []bool{false, true} {
		if drift := chanlib.CapImplReport(&bot{}, capsFor(credentialed)); len(drift) > 0 {
			t.Errorf("credentialed=%v cap/impl drift: %v", credentialed, drift)
		}
	}
	if capsFor(false)["send_text"] || !capsFor(true)["send_text"] {
		t.Error("send_text must follow the credential")
	}
	for _, credentialed := range []bool{false, true} {
		caps := capsFor(credentialed)
		if len(caps) != 1 {
			t.Errorf("credentialed=%v: googd advertises %v; send_text is the only verb (specs/6/45-gdrive-comments-channel.md §\"Decision 3 — one verb, the thread reply\")",
				credentialed, caps)
		}
		if _, ok := caps["send_text"]; !ok {
			t.Errorf("credentialed=%v: caps %v lack send_text", credentialed, caps)
		}
	}
}
