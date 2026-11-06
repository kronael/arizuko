package auth

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/groupfolder"
	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/store"
)

type Ask struct{ Action, Scope string }
type Mutation struct {
	Remove bool
	Row    core.ACLRow
	Parent string
}

var ErrGrantRefused = errors.New("grant refused")

func CheckGrant(s *store.Store, granter Caller, door Ask, mutations ...Mutation) (objstore.Version, error) {
	if s == nil || (granter.Principal == "" && granter.Route == "") {
		return "", ErrGrantRefused
	}
	return objstore.ReadConsistent(context.Background(), s.Obj(), store.OwnerScope(store.OwnerRoutd), func(context.Context) error {
		p, err := readPolicy(s, granter)
		if err != nil {
			return err
		}
		if door.Action != "" && !p.Allows(door.Action, door.Scope, nil) {
			return ErrGrantRefused
		}
		for _, m := range mutations {
			if m.Parent == "" {
				if m.Row.Principal != "" {
					if _, err := readPolicy(s, Caller{Principal: m.Row.Principal}); err != nil {
						return err
					}
				}
				if err := p.checkRow(m.Row, m.Remove, true); err != nil {
					return err
				}
				continue
			}
			parent, err := readPolicy(s, Caller{Principal: m.Parent})
			if err != nil {
				return err
			}
			for _, layer := range []struct {
				rows  []core.ACLRow
				local bool
			}{{parent.local, true}, {parent.defaults, false}} {
				for _, row := range layer.rows {
					if !m.Remove && row.Effect == store.EffectDeny && !strings.HasPrefix(row.Action, HoldPrefix) {
						continue
					}
					if m.Remove && row.Effect != store.EffectDeny && !layer.local && !strings.HasPrefix(row.Action, HoldPrefix) {
						continue
					}
					if err := p.checkRow(row, m.Remove, false); err != nil {
						return err
					}
				}
			}
		}
		return nil
	})
}

func (p Policy) checkRow(row core.ACLRow, remove, direct bool) error {
	if direct && !remove && row.Effect != store.EffectDeny && strings.ContainsAny(row.Principal, `*?[\`) {
		return ErrGrantRefused
	}
	if strings.HasPrefix(row.Action, HoldPrefix) {
		row.Action = "admin"
		row.Params = ""
		return p.checkAuthority(row, false)
	}
	if !remove && row.Effect == store.EffectDeny {
		return nil
	}
	return p.checkAuthority(row, true)
}

func (p Policy) checkAuthority(row core.ACLRow, option bool) error {
	rows := p.defaults
	for _, held := range p.local {
		if grantActionsMeet(held.Action, row.Action) && mayMeet(held.Scope, row.Scope) {
			rows = p.local
			break
		}
	}
	var allows []core.ACLRow
	for _, held := range rows {
		if !grantActionsMeet(held.Action, row.Action) || !mayMeet(held.Scope, row.Scope) {
			continue
		}
		if held.Effect == store.EffectDeny {
			return ErrGrantRefused
		}
		if (!option || held.GrantOption) && held.Predicate == "" && (p.caller.Route == "" || anyPrincipalMatches(held.Principal, p.route)) {
			allows = append(allows, held)
		}
	}
	if strings.HasPrefix(row.Action, ShellEnvPrefix) {
		for i := range allows {
			if allows[i].Action == "*" {
				allows[i].Action = ShellEnvPrefix + "*"
			}
		}
	}
	if !coveredByGrantable(allows, row) {
		return fmt.Errorf("%w: %s on %s", ErrGrantRefused, row.Action, row.Scope)
	}
	return nil
}

func grantActionsMeet(a, b string) bool {
	if a == "*" && strings.HasPrefix(b, ShellEnvPrefix) {
		return true
	}
	return actionCovers(a, b) || actionCovers(b, a)
}

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
