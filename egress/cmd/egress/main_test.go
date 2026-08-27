package main

import (
	"os"
	"reflect"
	"testing"
)

// splitCSV parses EGRESS_ALLOW / EGRESS_DENY. A parse bug here is a policy
// bug: a rule that silently becomes empty, or one that keeps its surrounding
// whitespace and therefore never matches, is an allowlist entry that does not
// protect anything. New's validation catches a malformed rule but cannot catch
// a rule that was dropped before it got there.
func TestSplitCSVTrimsAndDropsEmpties(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{",", nil},
		{"  ,  ,", nil},
		{"example.com", []string{"example.com"}},
		{" example.com ", []string{"example.com"}},
		{"a.example,b.example", []string{"a.example", "b.example"}},
		{"a.example, b.example ,, c.example", []string{"a.example", "b.example", "c.example"}},
		{"\ta.example\n", []string{"a.example"}},
	}
	for _, c := range cases {
		if got := splitCSV(c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("splitCSV(%q) = %#v, want %#v", c.in, got, c.want)
		}
	}
}

func TestEnvOrFallsBackOnUnsetAndEmpty(t *testing.T) {
	const key = "ARIZUKO_EGRESS_TEST_VAR"

	if err := os.Unsetenv(key); err != nil {
		t.Fatal(err)
	}
	if got := envOr(key, "fallback"); got != "fallback" {
		t.Errorf("unset: envOr = %q, want the default", got)
	}

	// An empty value is a variable someone set to nothing, which must read as
	// "not configured" rather than as an empty listen address.
	t.Setenv(key, "")
	if got := envOr(key, "fallback"); got != "fallback" {
		t.Errorf("empty: envOr = %q, want the default", got)
	}

	t.Setenv(key, "set")
	if got := envOr(key, "fallback"); got != "set" {
		t.Errorf("set: envOr = %q, want the value", got)
	}
}
