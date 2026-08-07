package auth

// Delegation subset-check (spec 5/33). Replaces tier-masking: a principal may
// grant onward only a SUBSET of the acl rows it HOLDS, and only those it holds
// WITH GRANT OPTION (grant_option=1). Postgres `GRANT … WITH GRANT OPTION`.
//
// One production call site: `add_acl` (`routd/acl_resource.go`), where the
// writer is not already root (`role:operator` holds `(*, **, grant_option=1)`
// and so delegates anything). Authority strictly decreases down every
// delegation chain without a depth number.
//
// NOT used at spawn. A subagent shares its parent's SO_PEERCRED socket and so
// holds the parent's grants exactly — a strict equal, not a subset. Downscoping
// a spawn was decided against in `specs/5/5` R4 (2026-08-07); it would need a
// second socket, which is the parallel path this codebase removes.

import (
	"fmt"
	"strings"

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
	denies := denyRows(s, granter)
	for _, r := range want {
		// Delegation grants capability; it never writes a deny (that is a
		// restriction, not a subset of what the granter holds) and never targets a
		// wildcard principal (which would hand the grant to every matching sub —
		// an escalation the subset-check must refuse).
		if r.Effect == "deny" {
			return fmt.Errorf("cannot delegate a deny row (%s %s on %s)", r.Principal, r.Action, r.Scope)
		}
		if strings.Contains(r.Principal, "*") {
			return fmt.Errorf("cannot delegate to a wildcard principal %q", r.Principal)
		}
		if !coveredByGrantable(held, r) {
			return fmt.Errorf("cannot delegate %s %s on %s: not held WITH GRANT OPTION",
				r.Principal, r.Action, r.Scope)
		}
		// The granter cannot delegate past its OWN deny: a deny it holds on
		// (action,scope) blocks it there, so it may not re-grant it away.
		if coveredByGrantable(denies, r) {
			return fmt.Errorf("cannot delegate %s on %s: granter holds a deny covering it",
				r.Action, r.Scope)
		}
	}
	return nil
}

// denyRows is granter's DENY rows over its expanded principal set — the rows a
// delegation may not grant past (deny-precedence, mirrored into the subset check).
func denyRows(s *store.Store, granter string) []core.ACLRow {
	principals := append([]string{granter}, s.Ancestors(granter)...)
	all := s.ACLRowsFor(principals)
	out := all[:0]
	for _, r := range all {
		if r.Effect == "deny" {
			out = append(out, r)
		}
	}
	return out
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
// glob) — held.Scope ⊇ want.Scope. Conservative: a `want` that introduces a
// wildcard `held` does not already span is REFUSED (else held `acme/*` would
// wrongly cover want `acme/**`, since path.Match("*","**") is true — the granter
// must not hand out authority over `acme/eng/sre` it does not itself hold).
//
//   - `**` covers everything.
//   - `<base>/**` covers base and any path under base (incl. deeper globs).
//   - any other held covers only a WILDCARD-FREE want that matches its pattern.
func scopeCovers(held, want string) bool {
	if held == want || held == "**" {
		return true
	}
	if base, ok := strings.CutSuffix(held, "/**"); ok {
		return want == base || strings.HasPrefix(want, base+"/")
	}
	// held carries no recursive tail: it can only cover a concrete want. A want
	// with its own wildcard might span beyond held, so refuse it outright.
	if strings.ContainsAny(want, "*") {
		return false
	}
	return matchPattern(held, want)
}

// paramsCover reports whether a held row's param spec permits a wanted row's.
// An empty held spec restricts nothing (covers any wanted spec); otherwise the
// specs must match exactly. Conservative — no partial-glob widening here.
func paramsCover(held, want string) bool {
	return held == "" || held == want
}
