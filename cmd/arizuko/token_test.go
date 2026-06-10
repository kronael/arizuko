package main

import (
	"testing"

	"github.com/kronael/arizuko/groupfolder"
)

func TestJidFolder(t *testing.T) {
	cases := []struct {
		jid  string
		want string
	}{
		{"web:solo", "solo"},
		{"web:acme/eng", "acme/eng"},                         // multi-segment folder
		{"web:acme/eng/submissions", "acme/eng/submissions"}, // whole rest; suffix needs explicit owner_folder
		{"web:", ""},
		{"hook:solo/gh-webhook", "solo"},
		{"hook:acme/eng/gh-webhook", "acme/eng"},
		{"hook:solo", "solo"}, // no source segment
		{"telegram:foo", ""},  // unrecognised prefix
		{"", ""},
	}
	for _, c := range cases {
		if got := groupfolder.JidFolder(c.jid); got != c.want {
			t.Errorf("JidFolder(%q) = %q, want %q", c.jid, got, c.want)
		}
	}
}
