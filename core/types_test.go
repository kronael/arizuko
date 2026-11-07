package core

import "testing"

func TestParseRouteTarget(t *testing.T) {
	for _, tc := range []struct {
		in, folder, mode, topic string
		announce, issues        bool
	}{
		{"rhias/nemo", "rhias/nemo", "", "", false, false},
		{"rhias/nemo?observe#engaged", "rhias/nemo", "observe", "engaged", false, false},
		{"sloth/morha?engaged#topic", "sloth/morha", "engaged", "topic", false, false},
		{"corp/eng?announce", "corp/eng", "", "", true, false},
		{"me/dm?issues", "me/dm", "", "", false, true},
		{"atlas#observe", "atlas", "", "observe", false, false},
	} {
		got := ParseRouteTarget(tc.in)
		if got.Folder != tc.folder || got.Mode != tc.mode || got.Topic != tc.topic || got.Announce != tc.announce || got.Issues != tc.issues || got.String() != tc.in {
			t.Errorf("ParseRouteTarget(%q) = %+v (%q)", tc.in, got, got.String())
		}
	}
}

func TestGenHexToken(t *testing.T) {
	seen := map[string]bool{}
	for range 64 {
		tk := GenHexToken()
		if len(tk) != 64 {
			t.Fatalf("want 64 chars, got %d", len(tk))
		}
		if seen[tk] {
			t.Fatal("duplicate token")
		}
		seen[tk] = true
	}
}
