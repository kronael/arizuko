package auth

// Delegation subset-check (spec 4/R). Replaces tier-masking: a principal may
// grant onward only a SUBSET of the acl rows it HOLDS, and only those it holds
// WITH GRANT OPTION (grant_option=1). Postgres `GRANT … WITH GRANT OPTION`.
//
// Used at spawn (a parent agent seeds a child's grants) and at any operator
// `add_acl` where the writer is not already root (`role:operator`, which holds
// `(*, **, grant_option=1)` and so delegates anything). Authority strictly
// decreases down every delegation chain without a depth number.

import (
	"fmt"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/store"
)

// Delegate reports whether granter may grant every row in want. It loads
// granter's held allow-rows carrying grant_option=1 (expanded transitively via
// acl_membership, so a member of role:operator inherits its `*`), and requires
// each wanted row to be covered by one of them. Returns nil if all are
// delegable, else an error naming the first row that is not.
//
// It is a pure precondition — it writes nothing. The caller writes the rows only
// after this returns nil.
func Delegate(s *store.Store, granter string, want []core.ACLRow) error {
	held := grantableRows(s, granter)
	for _, r := range want {
		if !coveredByGrantable(held, r) {
			return fmt.Errorf("cannot delegate %s %s on %s: not held WITH GRANT OPTION",
				r.Principal, r.Action, r.Scope)
		}
	}
	return nil
}

// grantableRows is granter's allow-rows with grant_option=1, over its expanded
// principal set (self + transitive membership ancestors). Deny rows and
// non-grant-option rows are excluded — you cannot delegate what you may not
// re-grant.
func grantableRows(s *store.Store, granter string) []core.ACLRow {
	principals := append([]string{granter}, s.Ancestors(granter)...)
	all := s.ACLRowsFor(principals)
	out := all[:0]
	for _, r := range all {
		if r.GrantOption && (r.Effect == "" || r.Effect == "allow") {
			out = append(out, r)
		}
	}
	return out
}

// coveredByGrantable reports whether some held grantable row covers want:
// action lattice covers, scope glob covers (want.Scope matches held.Scope's
// glob), params cover, and want does not claim a grant option the holder lacks.
// Conservative — anything not provably covered is rejected.
func coveredByGrantable(held []core.ACLRow, want core.ACLRow) bool {
	for _, h := range held {
		if !actionCovers(h.Action, want.Action) {
			continue
		}
		if !scopeCovers(h.Scope, want.Scope) {
			continue
		}
		if !paramsCover(h.Params, want.Params) {
			continue
		}
		// Can't mint an option you don't hold — h already has grant_option=1
		// (grantableRows filtered it), so any want.GrantOption is fine.
		return true
	}
	return false
}

// scopeCovers reports whether held (a folder glob) covers want (a folder path or
// glob) — held.Scope ⊇ want.Scope. Exact match, the `**`/`*` universal, or want
// matching held's glob pattern. A broader want than held is rejected because it
// won't match held's pattern (e.g. held `acme/eng` does not match want
// `acme/**`).
func scopeCovers(held, want string) bool {
	if held == want || held == "**" || held == "*" {
		return true
	}
	return matchPattern(held, want)
}

// paramsCover reports whether a held row's param spec permits a wanted row's.
// An empty held spec restricts nothing (covers any wanted spec); otherwise the
// specs must match exactly. Conservative — no partial-glob widening here.
func paramsCover(held, want string) bool {
	return held == "" || held == want
}
