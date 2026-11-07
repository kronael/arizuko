package core

import "testing"

func TestValidateRouteTarget(t *testing.T) {
	for _, target := range []string{"team/{sender}", "team?observe#{jid}", "team?engaged#{thread}", "team?announce#observe", "team?issues#fixed"} {
		if err := ValidateRouteTarget(target); err != nil {
			t.Errorf("%q: %v", target, err)
		}
	}
	for _, target := range []string{"", "team?", "team?bad", "team?observe=1", "team?observe&engaged", "team/{jid}", "team#{sender}", "team#{unknown}", "team#{jid", "https://example.org/x"} {
		if err := ValidateRouteTarget(target); err == nil {
			t.Errorf("accepted %q", target)
		}
	}
}
