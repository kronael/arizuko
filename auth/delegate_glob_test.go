package auth_test

import (
	"testing"

	"github.com/kronael/arizuko/core"
)

// BUGS L667: a held glob does not cover a wanted `?`/`[` glob, and a deny on a
// crossing glob blocks a grant it may meet.
func TestDelegateGlobContainment(t *testing.T) {
	row := func(p, a, sc, eff string, opt bool) core.ACLRow {
		return core.ACLRow{Principal: p, Action: a, Scope: sc, Effect: eff, GrantOption: opt}
	}
	t.Run("held a/??? does not cover a/[x]", func(t *testing.T) {
		s := openMem(t)
		addRowFull(t, s, row("folder:g", "mcp:x", "a/???", "allow", true))
		if err := delegateForTest(s, "folder:g", []core.ACLRow{row("folder:h", "mcp:x", "a/[x]", "allow", false)}); err == nil {
			t.Fatal("a/[x] matches a/x, which a/??? does not hold")
		}
	})
	t.Run("a deny on a/*/x blocks a/b/*", func(t *testing.T) {
		s := openMem(t)
		addRowFull(t, s, row("folder:k", "mcp:y", "a/**", "allow", true))
		addRowFull(t, s, row("folder:k", "mcp:y", "a/*/x", "deny", false))
		if err := delegateForTest(s, "folder:k", []core.ACLRow{row("folder:m", "mcp:y", "a/b/*", "allow", false)}); err == nil {
			t.Fatal("a/b/* meets the denied a/b/x")
		}
	})
	t.Run("a deny on a/b/x does not block a/c/*", func(t *testing.T) {
		s := openMem(t)
		addRowFull(t, s, row("folder:k", "mcp:y", "a/**", "allow", true))
		addRowFull(t, s, row("folder:k", "mcp:y", "a/b/x", "deny", false))
		if err := delegateForTest(s, "folder:k", []core.ACLRow{row("folder:m", "mcp:y", "a/c/*", "allow", false)}); err != nil {
			t.Fatalf("disjoint scopes must pass: %v", err)
		}
	})
}
