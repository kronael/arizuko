package rule

import (
	"strings"
	"testing"
)

func TestMatch(t *testing.T) {
	for _, tc := range []struct {
		pattern, tool string
		want          bool
	}{
		{"*", "anything", true},
		{"read_file", "read_file", true},
		{"read_file", "read_files", false},
		{"read_*", "read_file", true},
		{"read_*", "send_message", false},
		{"send_*", "send_message/attachment", true},
		{"*_file", "read_file", true},
		{"read_*_v2", "read_big_file_v2", true},
		{"read_*_v2", "read_big_file_v3", false},
	} {
		if got := Match(tc.pattern, tc.tool); got != tc.want {
			t.Errorf("Match(%q, %q) = %v, want %v", tc.pattern, tc.tool, got, tc.want)
		}
	}
}

// TestDecideDenyWins pins the rule that makes this a gate rather than a router:
// a later broad allow must never re-open a tool an earlier rule denied.
func TestDecideDenyWins(t *testing.T) {
	s := Set{Rules: []Rule{
		{Tool: "send_*", Action: Deny},
		{Tool: "*", Action: Allow},
	}}
	if s.Decide("send_message").Allowed {
		t.Error("deny did not dominate a later allow")
	}
	if !s.Decide("read_file").Allowed {
		t.Error("allow:* did not permit an undenied tool")
	}
}

// TestDecideDefaultDeny pins that an unlisted tool is refused, not passed.
func TestDecideDefaultDeny(t *testing.T) {
	s := Set{Rules: []Rule{{Tool: "read_*", Action: Allow}}}
	if s.Decide("delete_everything").Allowed {
		t.Error("a tool matched by no rule was allowed")
	}
}

// TestDecideLogDoesNotPermit: a log rule records and decides nothing.
func TestDecideLogDoesNotPermit(t *testing.T) {
	d := Set{Rules: []Rule{{Tool: "read_*", Action: Log}}}.Decide("read_file")
	if d.Allowed {
		t.Error("a log rule permitted a call on its own")
	}
	if !d.Logged {
		t.Error("a log rule did not mark the call logged")
	}
}

// TestLoadRejectsUnknownAction: a typo'd action must fail loudly. Skipping it
// would drop the rule, and a dropped deny is an opened tool.
func TestLoadRejectsUnknownAction(t *testing.T) {
	_, err := Load(strings.NewReader(`{"rules":[{"tool":"send_*","action":"allwo"}]}`))
	if err == nil {
		t.Fatal("Load accepted an unknown action")
	}
}

func TestLoadRejectsEmptyPattern(t *testing.T) {
	if _, err := Load(strings.NewReader(`{"rules":[{"tool":"","action":"deny"}]}`)); err == nil {
		t.Fatal("Load accepted an empty tool pattern")
	}
}
