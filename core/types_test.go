package core

import "testing"

func TestParseRouteTarget(t *testing.T) {
	for _, tc := range []struct {
		in, folder, mode, topic string
	}{
		{"rhias/nemo", "rhias/nemo", "", ""},
		{"rhias/nemo?observe#engaged", "rhias/nemo", "observe", "engaged"},
		{"sloth/morha?engaged#topic", "sloth/morha", "engaged", "topic"},
		{"corp/eng?announce", "corp/eng", "announce", ""},
		{"me/dm?issues", "me/dm", "issues", ""},
		{"atlas#observe", "atlas", "", "observe"},
	} {
		got := ParseRouteTarget(tc.in)
		if got.Folder != tc.folder || got.Mode != tc.mode || got.Topic != tc.topic || got.String() != tc.in {
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
