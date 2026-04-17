package auth

import "testing"

func TestMatchGroups(t *testing.T) {
	cases := []struct {
		name    string
		allowed []string
		folder  string
		want    bool
	}{
		{"empty", nil, "alice", false},
		{"double-star nested", []string{"**"}, "pub/a/b", true},
		{"literal match", []string{"alice"}, "alice", true},
		{"literal mismatch", []string{"alice"}, "bob", false},
		{"glob one segment", []string{"pub/*"}, "pub/foo", true},
		{"glob no cross slash", []string{"pub/*"}, "pub/foo/bar", false},
		{"multi entry first", []string{"alice", "pub/*"}, "alice", true},
		{"multi entry second", []string{"alice", "pub/*"}, "pub/x", true},
		{"world doublestar one", []string{"world/**"}, "world/a", true},
		{"world doublestar deep", []string{"world/**"}, "world/a/b/c", true},
		{"world doublestar root", []string{"world/**"}, "world", true},
		{"world doublestar miss", []string{"world/**"}, "other/a", false},
		{"mid doublestar", []string{"w/**/leaf"}, "w/a/b/leaf", true},
		{"mid doublestar no leaf", []string{"w/**/leaf"}, "w/a/b/c", false},
	}
	for _, c := range cases {
		if got := MatchGroups(c.allowed, c.folder); got != c.want {
			t.Errorf("%s: MatchGroups(%v, %q) = %v, want %v",
				c.name, c.allowed, c.folder, got, c.want)
		}
	}
}
