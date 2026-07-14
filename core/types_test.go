package core

import "testing"

func TestParseRouteTarget(t *testing.T) {
	cases := []struct {
		in       string
		folder   string
		topic    string
		mode     string
		announce bool
	}{
		{"rhias/nemo", "rhias/nemo", "", "", false},
		{"rhias/nemo#observe", "rhias/nemo", "", "observe", false},
		{"main#observe", "main", "", "observe", false},
		{"corp/eng#announce", "corp/eng", "", "", true},
		{"main#announce", "main", "", "", true},
		{"", "", "", "", false},
		{"a#", "a", "", "", false},
		{"a#b#c", "a", "b#c", "", false},
		{"atlas#oncall", "atlas", "oncall", "", false},
	}
	for _, c := range cases {
		got := ParseRouteTarget(c.in)
		if got.Folder != c.folder || got.Topic != c.topic || got.Mode != c.mode || got.Announce != c.announce {
			t.Errorf("ParseRouteTarget(%q) = %+v, want folder=%q topic=%q mode=%q announce=%v",
				c.in, got, c.folder, c.topic, c.mode, c.announce)
		}
		round := got.String()
		want := c.folder
		switch {
		case c.mode != "":
			want = c.folder + "#" + c.mode
		case c.announce:
			want = c.folder + "#announce"
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
	for i := 0; i < 64; i++ {
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
