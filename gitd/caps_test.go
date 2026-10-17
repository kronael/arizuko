package main

import (
	"testing"

	"github.com/kronael/arizuko/chanlib"
)

func TestCapImplConsistency(t *testing.T) {
	for _, token := range []string{"", "ghp_x"} {
		if drift := chanlib.CapImplReport(&bot{cfg: config{Token: token}}, capsFor(token)); len(drift) > 0 {
			t.Errorf("token=%q cap/impl drift: %v", token, drift)
		}
	}
	for _, cap := range []string{"send_text", "typing", "edit"} {
		if capsFor("")[cap] || !capsFor("ghp_x")[cap] {
			t.Errorf("%s must follow GITHUB_TOKEN presence", cap)
		}
	}
	for cap := range capsFor("ghp_x") {
		if cap != "send_text" && cap != "typing" && cap != "edit" {
			t.Errorf("gitd advertises %q; send_text, typing and edit are all it has (specs/6/46-turn-progress.md \"What this changes in 6/27 and 6/45\")", cap)
		}
	}
}
