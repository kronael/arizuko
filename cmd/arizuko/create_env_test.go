package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCreateEnvNamesEgressIsolation: the `.env` `arizuko create` generates has
// to NAME the egress switch and say what its empty value allows.
//
// `create` writes its own key set and never reads `template/env.example`, so
// the warning that file carries reached nobody on the documented install route
// — a fresh instance ran unfiltered while SECURITY.md described the isolated
// shape (BUGS L24). The assertion is on discoverability, not on the default:
// the value stays empty, and whether it should is the owner's open question.
func TestCreateEnvNamesEgressIsolation(t *testing.T) {
	t.Cleanup(func() { dropInstance(t) })
	base := t.TempDir()
	t.Setenv("ARIZUKO_DATA_DIR", base)
	t.Setenv("DATA_DIR", "")

	cmdCreate([]string{"egressinst"})

	b, err := os.ReadFile(filepath.Join(base, "arizuko_egressinst", ".env"))
	if err != nil {
		t.Fatalf("create wrote no .env: %v", err)
	}
	env := string(b)

	if !strings.Contains(env, "\nEGRESS_IMAGE=\n") {
		t.Errorf("generated .env does not name EGRESS_IMAGE; an operator never\n"+
			"learns the switch exists:\n%s", env)
	}
	// The bare key alone is not the fix: an operator who has never read spec
	// 6/24 cannot tell that empty means unfiltered.
	if !strings.Contains(env, "UNFILTERED") {
		t.Errorf("generated .env does not say what an empty EGRESS_IMAGE allows:\n%s", env)
	}
	// The DEFAULT is deliberately untouched — flipping it is the half of L24
	// that needs sign-off, and a test that asserted an image here would ship
	// that decision silently.
	for _, line := range strings.Split(env, "\n") {
		if strings.HasPrefix(line, "EGRESS_IMAGE=") && line != "EGRESS_IMAGE=" {
			t.Errorf("create seeded an egress image (%q); turning isolation on by\n"+
				"default is BUGS L24's unsigned half, not a docs fix", line)
		}
	}
}
