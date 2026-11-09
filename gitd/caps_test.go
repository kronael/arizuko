package main

import (
	"testing"

	"github.com/kronael/arizuko/chanlib"
)

func TestCapImplConsistency(t *testing.T) {
	if drift := chanlib.CapImplReport(&bot{cfg: config{Token: "ghp_x"}}, capsFor("ghp_x")); len(drift) > 0 {
		t.Errorf("cap/impl drift: %v", drift)
	}
	for _, cap := range []string{"send_text", "send_file", "typing", "edit", "delete"} {
		if capsFor("")[cap] || !capsFor("ghp_x")[cap] {
			t.Errorf("%s must follow GITHUB_TOKEN presence", cap)
		}
	}
	for cap := range capsFor("ghp_x") {
		if cap != "send_text" && cap != "send_file" && cap != "typing" && cap != "edit" && cap != "delete" {
			t.Errorf("gitd advertises unsupported capability %q", cap)
		}
	}
}
