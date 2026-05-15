package core

import "testing"

func TestParseRouteTarget(t *testing.T) {
	cases := []struct {
		in     string
		folder string
		mode   string
	}{
		{"rhias/nemo", "rhias/nemo", ""},
		{"rhias/nemo#observe", "rhias/nemo", "observe"},
		{"main#observe", "main", "observe"},
		{"", "", ""},
		{"a#", "a", ""},
		{"a#b#c", "a", "b#c"},
	}
	for _, c := range cases {
		got := ParseRouteTarget(c.in)
		if got.Folder != c.folder || got.Mode != c.mode {
			t.Errorf("ParseRouteTarget(%q) = %+v, want folder=%q mode=%q",
				c.in, got, c.folder, c.mode)
		}
		round := got.String()
		// String() emits "folder" when mode is empty, "folder#mode" otherwise.
		want := c.folder
		if c.mode != "" {
			want = c.folder + "#" + c.mode
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
