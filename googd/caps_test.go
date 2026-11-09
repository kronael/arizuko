package main

import (
	"testing"

	"github.com/kronael/arizuko/chanlib"
)

func TestCapImplConsistency(t *testing.T) {
	if drift := chanlib.CapImplReport(&bot{}, capsFor(true)); len(drift) > 0 {
		t.Errorf("cap/impl drift: %v", drift)
	}
	for _, capability := range []string{"send_text", "edit", "delete"} {
		if capsFor(false)[capability] || !capsFor(true)[capability] {
			t.Errorf("%s must follow the credential", capability)
		}
	}
	if len(capsFor(true)) != 3 {
		t.Errorf("unexpected capabilities: %v", capsFor(true))
	}
}
