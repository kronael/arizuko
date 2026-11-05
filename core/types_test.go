package core

import "testing"

func TestParseRouteTarget(t *testing.T) {
	cases := []struct {
		in       string
		folder   string
		topic    string
		mode     string
		announce bool
		issues   bool
	}{
		{"rhias/nemo", "rhias/nemo", "", "", false, false},
		{"rhias/nemo#observe", "rhias/nemo", "", "observe", false, false},
		{"main#observe", "main", "", "observe", false, false},
		{"corp/eng#announce", "corp/eng", "", "", true, false},
		{"main#announce", "main", "", "", true, false},
		{"me/dm#issues", "me/dm", "", "", false, true},
		{"sloth/morha#engaged", "sloth/morha", "", "engaged", false, false},
		{"", "", "", "", false, false},
		{"a#", "a", "", "", false, false},
		{"a#b#c", "a", "b#c", "", false, false},
		{"atlas#oncall", "atlas", "oncall", "", false, false},
	}
	for _, c := range cases {
		got := ParseRouteTarget(c.in)
		if got.Folder != c.folder || got.Topic != c.topic || got.Mode != c.mode || got.Announce != c.announce || got.Issues != c.issues {
			t.Errorf("ParseRouteTarget(%q) = %+v, want folder=%q topic=%q mode=%q announce=%v issues=%v",
				c.in, got, c.folder, c.topic, c.mode, c.announce, c.issues)
		}
		round := got.String()
		want := c.folder
		switch {
		case c.mode != "":
			want = c.folder + "#" + c.mode
		case c.announce:
			want = c.folder + "#announce"
		case c.issues:
			want = c.folder + "#issues"
		case c.topic != "":
			want = c.folder + "#" + c.topic
		}
		if round != want {
			t.Errorf("ParseRouteTarget(%q).String() = %q, want %q", c.in, round, want)
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
