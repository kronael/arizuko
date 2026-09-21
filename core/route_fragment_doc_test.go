package core

// A route target's fragment is an operator-facing vocabulary, and ROUTING.md is
// where an operator reads it. Every fragment ParseRouteTarget reserves is one
// name a topic can no longer carry — reserving `announce` in v0.59 silently
// reinterpreted any route that already pinned that topic, and a route row has no
// creation time, so nothing can tell an old topic from a new fragment after the
// fact (BUGS R2). ROUTING.md's table then listed `#observe` and stopped, so the
// one place an operator could have learned that `announce` was taken did not say
// so for two months.
//
// This reads the reserved names out of ParseRouteTarget's own source and demands
// each is documented. The next reserved fragment cannot reach the parser without
// reaching the operator.

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// parseRouteTargetCases captures the `case "x":` labels inside ParseRouteTarget.
var parseRouteTargetCases = regexp.MustCompile(`case\s+"([^"]+)":`)

// reservedFragments reads the fragments ParseRouteTarget treats as reserved,
// from the function's real source rather than a copy that can go stale.
func reservedFragments(t *testing.T) []string {
	t.Helper()
	b, err := os.ReadFile("types.go")
	if err != nil {
		t.Fatalf("read core/types.go: %v", err)
	}
	src := string(b)
	start := strings.Index(src, "func ParseRouteTarget(")
	if start < 0 {
		t.Fatal("ParseRouteTarget not found in core/types.go — this test reads its source")
	}
	end := strings.Index(src[start:], "\nfunc ")
	if end < 0 {
		t.Fatal("could not find the end of ParseRouteTarget")
	}
	var out []string
	for _, m := range parseRouteTargetCases.FindAllStringSubmatch(src[start:start+end], -1) {
		out = append(out, m[1])
	}
	if len(out) == 0 {
		t.Fatal("no reserved fragment found in ParseRouteTarget — the regexp no longer matches its shape")
	}
	return out
}

// TestReservedFragmentsAreDocumented fails when the parser reserves a fragment
// ROUTING.md does not name.
func TestReservedFragmentsAreDocumented(t *testing.T) {
	doc, err := os.ReadFile(filepath.Join("..", "ROUTING.md"))
	if err != nil {
		t.Fatalf("read ROUTING.md: %v", err)
	}
	routing := string(doc)

	for _, frag := range reservedFragments(t) {
		// The reservation is what the doc must explain: the fragment does not
		// become a topic.
		if got := ParseRouteTarget("demo#" + frag); got.Topic != "" {
			t.Errorf("ParseRouteTarget(%q) pinned topic %q — this test only covers reserved fragments", "demo#"+frag, got.Topic)
			continue
		}
		if !strings.Contains(routing, "`#"+frag+"`") {
			t.Errorf("ParseRouteTarget reserves %q but ROUTING.md never names `#%s`: "+
				"an operator reading the fragment table cannot learn that topic %q is taken",
				frag, frag, frag)
		}
	}
}
