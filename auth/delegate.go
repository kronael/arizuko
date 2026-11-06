package auth

// Delegation subset-check (spec 5/33-paths-roles). Replaces tier-masking: a principal may
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
// a spawn was decided against in spec 5/5-worlds-agents-sessions R4 (2026-08-07); it would need a
// second socket, which is the parallel path this codebase removes.

import (
	"fmt"
	"slices"
	"strings"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/groupfolder"
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
	// A policy read failure REFUSES the delegation. Delegate is a precondition on
	// a write that hands out authority: if what the granter holds cannot be read,
	// nothing is provably a subset of it, so the honest answer is no (BUGS A4).
	held, err := grantableRows(s, granter)
	if err != nil {
		return fmt.Errorf("cannot check delegation: %w", err)
	}
	denies, err := denyRows(s, granter)
	if err != nil {
		return fmt.Errorf("cannot check delegation: %w", err)
	}
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
		if blockedByDeny(denies, r) {
			return fmt.Errorf("cannot delegate %s on %s: granter holds a deny covering it",
				r.Action, r.Scope)
		}
	}
	return nil
}

// policyFor is granter's rows by the evaluator's own read (Authorize's
// policyRows over the expanded principal set), so a membership ancestor and a
// wildcard principal reach Delegate exactly as they reach Authorize.
func policyFor(s *store.Store, granter string) ([]core.ACLRow, error) {
	expanded, err := expandPrincipals(s, Caller{Principal: granter})
	if err != nil {
		return nil, err
	}
	return policyRows(s, expanded)
}

// denyRows is granter's DENY rows — the rows a delegation may not grant past
// (deny-precedence, mirrored into the subset check).
func denyRows(s *store.Store, granter string) ([]core.ACLRow, error) {
	all, err := policyFor(s, granter)
	if err != nil {
		return nil, err
	}
	var out []core.ACLRow
	for _, r := range all {
		if r.Effect == "deny" {
			out = append(out, r)
		}
	}
	return out, nil
}

// grantableRows is granter's allow-rows with grant_option=1. Deny rows,
// non-grant-option rows and conditional (predicate) rows are excluded — you
// cannot delegate what you may not re-grant, and a row that holds only under a
// claim does not cover an unconditional one.
func grantableRows(s *store.Store, granter string) ([]core.ACLRow, error) {
	all, err := policyFor(s, granter)
	if err != nil {
		return nil, err
	}
	var out []core.ACLRow
	for _, r := range all {
		if r.GrantOption && r.Predicate == "" && (r.Effect == "" || r.Effect == "allow") {
			out = append(out, r)
		}
	}
	return out, nil
}

// blockedByDeny reports whether some deny the granter holds OVERLAPS want: the
// deny's scope lies inside want's, or want's inside the deny's, and the actions
// overlap. A narrower deny inside a broader requested grant blocks it, since
// the grant would hand the delegate the slice the granter is denied. Params
// and predicate are ignored: a deny that may apply is treated as applying.
func blockedByDeny(denies []core.ACLRow, want core.ACLRow) bool {
	for _, d := range denies {
		if !actionCovers(d.Action, want.Action) && !actionCovers(want.Action, d.Action) {
			continue
		}
		if mayMeet(d.Scope, want.Scope) {
			return true
		}
	}
	return false
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
		return groupfolder.Contains(base, want)
	}
	// held carries no recursive tail: it can only cover a concrete want. A want
	// with its own glob character might span beyond held (`?` and `[` widen as
	// `*` does), so refuse it outright.
	if strings.ContainsAny(want, `*?[\`) {
		return false
	}
	return matchPattern(held, want)
}

// mayMeet reports whether two scope globs can match one path. It answers false
// only on proof: before the first `**` of either glob, segments align one to
// one, so two segments without a glob character that differ there keep the
// globs apart; and two globs without `**` match paths of their own length only.
func mayMeet(a, b string) bool {
	as, bs := strings.Split(a, "/"), strings.Split(b, "/")
	for i := 0; i < len(as) && i < len(bs); i++ {
		if as[i] == "**" || bs[i] == "**" {
			return true
		}
		if !strings.ContainsAny(as[i], `*?[\`) && !strings.ContainsAny(bs[i], `*?[\`) && as[i] != bs[i] {
			return false
		}
	}
	return len(as) == len(bs) || slices.Contains(as, "**") || slices.Contains(bs, "**")
}

// paramsCover reports whether a held row's param spec permits a wanted row's.
// An empty held spec restricts nothing (covers any wanted spec); otherwise the
// specs must match exactly. Conservative — no partial-glob widening here.
func paramsCover(held, want string) bool {
	return held == "" || held == want
}
