package auth

import "testing"

func TestMatchGroups(t *testing.T) {
	cases := []struct {
		name    string
		allowed []string
		folder  string
		want    bool
	}{
		{"nil", nil, "alice", false},
		{"empty", []string{}, "alice", false},
		{"double-star matches root", []string{"**"}, "alice", true},
		{"double-star matches nested", []string{"**"}, "pub/alice/nested", true},
		{"literal match", []string{"alice"}, "alice", true},
		{"literal no match", []string{"alice"}, "bob", false},
		{"glob matches one segment", []string{"pub/*"}, "pub/foo", true},
		{"glob does not cross slashes", []string{"pub/*"}, "pub/foo/bar", false},
		{"glob other prefix rejected", []string{"pub/*"}, "priv/foo", false},
		{"multi entry literal", []string{"alice", "pub/*"}, "alice", true},
		{"multi entry glob", []string{"alice", "pub/*"}, "pub/x", true},
		{"multi entry neither", []string{"alice", "pub/*"}, "bob", false},
		{"case sensitive", []string{"alice"}, "Alice", false},
	}
	for _, c := range cases {
		if got := MatchGroups(c.allowed, c.folder); got != c.want {
			t.Errorf("%s: MatchGroups(%v, %q) = %v, want %v",
				c.name, c.allowed, c.folder, got, c.want)
		}
	}
}
