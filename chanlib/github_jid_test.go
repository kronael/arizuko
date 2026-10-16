package chanlib

import "testing"

func TestParseGitHubJID(t *testing.T) {
	cases := []struct {
		jid  string
		want GitHubJID
		ok   bool
	}{
		{"github:kronael", GitHubJID{Owner: "kronael"}, true},
		{"github:kronael/arizuko", GitHubJID{Owner: "kronael", Repo: "arizuko"}, true},
		{"github:kronael/arizuko/pr/12", GitHubJID{Owner: "kronael", Repo: "arizuko", Kind: "pr", Number: 12}, true},
		{"github:kronael/arizuko/issue/3", GitHubJID{Owner: "kronael", Repo: "arizuko", Kind: "issue", Number: 3}, true},
		{"github:", GitHubJID{}, false},
		{"github:/arizuko", GitHubJID{}, false},
		{"github:kronael/", GitHubJID{}, false},
		{"github:kronael//pr/1", GitHubJID{}, false},
		{"github:kronael/arizuko/pr", GitHubJID{}, false},
		{"github:kronael/arizuko/commit/abc", GitHubJID{}, false},
		{"github:kronael/arizuko/pr/x", GitHubJID{}, false},
		{"github:kronael/arizuko/pr/0", GitHubJID{}, false},
		{"github:kronael/arizuko/pr/-1", GitHubJID{}, false},
		{"github:kronael/arizuko/pr/1/extra", GitHubJID{}, false},
		{"slack:kronael/arizuko", GitHubJID{}, false},
		{"kronael/arizuko", GitHubJID{}, false},
		{"", GitHubJID{}, false},
	}
	for _, c := range cases {
		got, ok := ParseGitHubJID(c.jid)
		if ok != c.ok || got != c.want {
			t.Errorf("ParseGitHubJID(%q) = %+v, %v; want %+v, %v", c.jid, got, ok, c.want, c.ok)
		}
		if ok && got.String() != c.jid {
			t.Errorf("round trip %q -> %q", c.jid, got.String())
		}
	}
}

func TestFormatGitHubJID(t *testing.T) {
	cases := []struct {
		owner, repo, kind string
		number            int
		want              string
	}{
		{"kronael", "", "", 0, "github:kronael"},
		{"kronael", "arizuko", "", 0, "github:kronael/arizuko"},
		{"kronael", "arizuko", "pr", 12, "github:kronael/arizuko/pr/12"},
		{"kronael", "arizuko", "issue", 3, "github:kronael/arizuko/issue/3"},
	}
	for _, c := range cases {
		if got := FormatGitHubJID(c.owner, c.repo, c.kind, c.number); got != c.want {
			t.Errorf("FormatGitHubJID(%q,%q,%q,%d) = %q, want %q", c.owner, c.repo, c.kind, c.number, got, c.want)
		}
	}
}

func TestGitHubJIDTopic(t *testing.T) {
	cases := map[string]string{
		"github:kronael":                 "",
		"github:kronael/arizuko":         "",
		"github:kronael/arizuko/pr/12":   "pr/12",
		"github:kronael/arizuko/issue/3": "issue/3",
	}
	for jid, want := range cases {
		j, ok := ParseGitHubJID(jid)
		if !ok {
			t.Fatalf("parse %q", jid)
		}
		if got := j.Topic(); got != want {
			t.Errorf("Topic(%q) = %q, want %q", jid, got, want)
		}
	}
}
