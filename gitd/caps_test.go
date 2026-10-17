package main

import (
	"testing"

	"github.com/kronael/arizuko/chanlib"
)

func TestCapImplConsistency(t *testing.T) {
	for _, token := range []string{"", "ghp_x"} {
		if drift := chanlib.CapImplReport(&bot{}, capsFor(token)); len(drift) > 0 {
			t.Errorf("token=%q cap/impl drift: %v", token, drift)
		}
	}
	for _, cap := range []string{"send_text", "typing"} {
		if capsFor("")[cap] || !capsFor("ghp_x")[cap] {
			t.Errorf("%s must follow GITHUB_TOKEN presence", cap)
		}
	}
	for cap := range capsFor("ghp_x") {
		if cap != "send_text" && cap != "typing" {
			t.Errorf("gitd advertises %q; send_text and typing are all it has (specs/6/27-git-channel.md § Channel messages and repository tools)", cap)
		}
	}
}
