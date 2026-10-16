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
	if capsFor("")["send_text"] || !capsFor("ghp_x")["send_text"] {
		t.Error("send_text must follow GITHUB_TOKEN presence")
	}
	for cap := range capsFor("ghp_x") {
		if cap != "send_text" {
			t.Errorf("gitd advertises %q; send_text is the only verb (specs/6/27-git-channel.md § Channel messages and repository tools)", cap)
		}
	}
}
