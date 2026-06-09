package routd

import "testing"

// ParseVhostAliases keeps valid host=world entries (lowercased host) and skips
// malformed / invalid ones loudly — a typo'd alias must never misreport a
// folder's web presence (BUG #6).
func TestParseVhostAliases(t *testing.T) {
	got := ParseVhostAliases("fab.krons.cx=atlas, FOO.example.com=bar/sub ,bad,=x,y=, ")
	want := map[string]string{"fab.krons.cx": "atlas", "foo.example.com": "bar/sub"}
	if len(got) != len(want) {
		t.Fatalf("parsed %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("alias[%q] = %q, want %q", k, got[k], v)
		}
	}

	// invalid hostname (space) + invalid world (`..`) are dropped; valid survives.
	got = ParseVhostAliases("ok.example.com=atlas,bad host=x,foo.com=../escape")
	if len(got) != 1 || got["ok.example.com"] != "atlas" {
		t.Fatalf("validation: got %v, want only {ok.example.com:atlas}", got)
	}

	if m := ParseVhostAliases(""); len(m) != 0 {
		t.Errorf("empty input → %v, want empty map", m)
	}
}
