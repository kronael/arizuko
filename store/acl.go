package store

// acl.go holds every read and write of `acl`: one item per grant, addressed by
// the six fields that ARE the grant.
//
// The partition is OwnerRoutd because resreg's entity path writes these items
// too (resreg.SubsystemRoutd IS store.OwnerRoutd); a second spelling selects
// nothing, silently. All six fields are in the coordinate because each changes
// what the row PERMITS. The principal leads, because the evaluator's hot read
// is one begins_with per principal. ListACLByScope and ACLWildcardRows read the
// partition and filter in Go: it is 42 rows on krons, and no index answers a
// substring.
//
// A READ FAILURE IS AN ERROR, NEVER AN EMPTY POLICY (BUGS A4): a partial scan
// could drop a DENY row while keeping the allows above it.

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/kronael/arizuko/audit"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
)

// The attributes a grant item carries. The six coordinate fields are NOT among
// them: storing the coordinate twice is two facts that can disagree.
const (
	attrACLGrantedBy   = "granted_by"
	attrACLGrantedAt   = "granted_at"
	attrACLGrantOption = "grant_option"
)

// aclKeyFields is the coordinate, in order, for the error a refusal prints.
const aclKeyFields = "principal,action,scope,params,predicate,effect"

// aclKey is the coordinate of one grant. A value carrying the separator is
// REFUSED: two grants would address one item (resreg/entity.go sortKeyFor
// makes the same refusal). An EMPTY part is allowed, because the separator
// count is fixed.
func aclKey(r core.ACLRow) (objstore.Key, error) {
	parts := []string{r.Principal, r.Action, r.Scope, r.Params, r.Predicate, r.Effect}
	for i, v := range parts {
		if strings.Contains(v, KeySep) {
			return objstore.Key{}, fmt.Errorf("acl row carries %s=%q, and that byte "+
				"separates the six fields of its coordinate (%s); two rows would address "+
				"one item", strings.Split(aclKeyFields, ",")[i], v, aclKeyFields)
		}
	}
	if r.Principal == "" || r.Action == "" || r.Scope == "" {
		return objstore.Key{}, fmt.Errorf("acl row carries principal=%q action=%q scope=%q; "+
			"a grant with an empty one of those names nothing", r.Principal, r.Action, r.Scope)
	}
	return objstore.Key{Partition: OwnerRoutd, Sort: strings.Join(parts, KeySep)}, nil
}

// aclRowFrom reads one item back. A key that does not split into six is a LOUD
// fault: an evaluator reading it would answer about a call nobody granted.
func aclRowFrom(it objstore.Item) (core.ACLRow, error) {
	parts := strings.Split(it.Key.Sort, KeySep)
	if len(parts) != 6 {
		return core.ACLRow{}, fmt.Errorf("acl item %q splits into %d parts and its "+
			"coordinate (%s) has 6", it.Key.Sort, len(parts), aclKeyFields)
	}
	return core.ACLRow{
		Principal:   parts[0],
		Action:      parts[1],
		Scope:       parts[2],
		Params:      parts[3],
		Predicate:   parts[4],
		Effect:      parts[5],
		GrantedBy:   it.Attrs[attrACLGrantedBy],
		GrantedAt:   it.Attrs[attrACLGrantedAt],
		GrantOption: it.Attrs[attrACLGrantOption] == "1",
	}, nil
}

// aclAttrs renders the three attributes a grant item carries. `grant_option` is
// ALWAYS written, as "0" or "1": the text resreg's bool encoding renders
// (resreg/codec.go), because both writers must agree byte for byte.
// `granted_at` is core.TS at 30 bytes, for the same reason (BUGS L97).
func aclAttrs(r core.ACLRow) map[string]string {
	attrs := map[string]string{attrACLGrantOption: "0"}
	if r.GrantOption {
		attrs[attrACLGrantOption] = "1"
	}
	setAttr(attrs, attrACLGrantedBy, r.GrantedBy)
	setAttr(attrs, attrACLGrantedAt, r.GrantedAt)
	return attrs
}

// The two effect spellings an acl row may carry. ONE declaration: auth reads
// the same pair.
const (
	EffectAllow = "allow"
	EffectDeny  = "deny"
)

// HoldPrefix opens the namespace a hold rule's action lives in: `hold:mcp:reply`
// answers "must a call to `reply` wait for a human?", never "may it run". It is
// here because the seed floor writes these actions; auth reads the same
// constant.
const HoldPrefix = "hold:mcp:"

// ShellEnvPrefix names the grant that puts a folder secret into the agent's
// shell. Here for HoldPrefix's reason; auth aliases it.
const ShellEnvPrefix = "shell:env:"

// EffectAllowed reports whether `effect` is a spelling every evaluator reads.
func EffectAllowed(effect string) bool {
	return effect == EffectAllow || effect == EffectDeny
}

// aclPut renders the create-if-absent write both acl writers issue: a re-grant
// must not restamp who granted a row or clear a grant option it holds.
//
// The effect is validated HERE because every door renders through this
// function: the evaluator reads anything but EffectDeny as a grant, so `denny`
// became an allow (BUGS L272).
func aclPut(r core.ACLRow) (objstore.Write, error) {
	if !EffectAllowed(r.Effect) {
		return objstore.Write{}, fmt.Errorf(
			"acl: effect %q on action %q is not a spelling any evaluator reads; "+
				"use %q or %q", r.Effect, r.Action, EffectAllow, EffectDeny)
	}
	// A predicate on a shell:env: row can only mean "never": ShellEnvKeys
	// (auth/authorize.go) resolves per folder with no Claims, so the key
	// silently stops appearing and nothing logs a denial (BUGS L134).
	if r.Predicate != "" && strings.HasPrefix(r.Action, ShellEnvPrefix) {
		return objstore.Write{}, fmt.Errorf(
			"acl: a predicate on %q can never match — the shell-secret resolver is "+
				"keyed on the folder and carries no claims, so %q would revoke the key "+
				"on every channel rather than narrow it; scope the grant to an exact "+
				"folder instead", r.Action, r.Predicate)
	}
	key, err := aclKey(r)
	if err != nil {
		return objstore.Write{}, err
	}
	// Every attribute is Init, and the op is a Merge rather than a guarded Put
	// so it has NOTHING TO REFUSE: a folder creation's admin grant rides the
	// same batch as the edge and the groups row.
	return objstore.Write{
		Op: objstore.Merge, Entity: ACL, Key: key, Init: aclAttrs(r),
	}, nil
}

// ACLGrantWrite is aclPut for the daemons that assemble a folder creation's
// whole batch themselves, with the defaults every other door fills.
func ACLGrantWrite(row core.ACLRow) (objstore.Write, error) { return aclPut(aclDefaults(row)) }

// AdminGrantWrite renders the grant that makes `principal` the admin of a
// folder it has just created. The scope is `folder/**`: matchSegments
// (auth/acl.go) ends on `len(in) == 0`, so a bare `eng` covers `eng` and
// REFUSES `eng/sre`, which left a creator unable to administer a child of its
// own folder (BUGS L206). One renderer, so no second shape drifts.
func AdminGrantWrite(principal, folder, grantedBy string) (objstore.Write, error) {
	return ACLGrantWrite(core.ACLRow{
		Principal: principal, Action: "admin", Scope: folder + "/**",
		Effect: EffectAllow, GrantedBy: grantedBy,
	})
}

// aclDefaults fills what a caller may leave out: the effect and the instant.
func aclDefaults(r core.ACLRow) core.ACLRow {
	if r.Effect == "" {
		r.Effect = EffectAllow
	}
	if r.GrantedAt == "" {
		r.GrantedAt = core.NowTS()
	}
	return r
}

// GrantDefaults applies the defaults a GRANT WRITER fills in: action `admin`,
// effect `allow`. routd's acl resource and `arizuko group grant`/`ungrant`
// share it, so the revoke addresses the coordinate the grant wrote (BUGS L138)
// and auth.Delegate judges the row the write produces (BUGS L93).
//
// It is NOT aclDefaults: that one fills no action, and merging them would
// default it to `admin` for three callers, which is a decision (BUGS L345).
func GrantDefaults(row core.ACLRow) core.ACLRow {
	if row.Action == "" {
		row.Action = "admin"
	}
	if row.Effect == "" {
		row.Effect = EffectAllow
	}
	return row
}

// AddACLRow grants a row idempotently and records it.
func (s *Store) AddACLRow(row core.ACLRow) error {
	row = aclDefaults(row)
	w, err := aclPut(row)
	if err != nil {
		return err
	}
	// A refusal is the grant already being there, and it records nothing.
	_, err = s.tryWriteAudited(func() ([]objstore.Write, audit.Event, error) {
		// GrantedBy stands in as the actor for callers that never set AsUser
		// (the CLI, onbod invite-accept).
		actor, actorSub, surface := s.auditIdentity()
		if s.auditSub == "" && row.GrantedBy != "" {
			actor, actorSub = row.GrantedBy, row.GrantedBy
		}
		return []objstore.Write{w}, audit.Event{
			Category: audit.CategoryAuthZ,
			Action:   "acl.add",
			Actor:    actor,
			ActorSub: actorSub,
			Surface:  surface,
			Resource: fmt.Sprintf("acl/%s/%s/%s", row.Principal, row.Action, row.Scope),
			Folder:   row.Scope,
			Outcome:  audit.OutcomeOK,
			ParamsSummary: map[string]any{
				"principal": row.Principal,
				"action":    row.Action,
				"scope":     row.Scope,
				"effect":    row.Effect,
				"predicate": row.Predicate,
				// Distinguishes a grant an operator typed from one a package
				// install brought in; the actor alone cannot.
				"granted_by": row.GrantedBy,
			},
		}, nil
	})
	return err
}

func (s *Store) RemoveACLRow(row core.ACLRow) error {
	if row.Effect == "" {
		row.Effect = EffectAllow
	}
	key, err := aclKey(row)
	if err != nil {
		return err
	}
	return s.writeAudited(func() ([]objstore.Write, audit.Event, error) {
		w := objstore.Write{Op: objstore.Delete, Entity: ACL, Key: key}
		actor, actorSub, surface := s.auditIdentity()
		return []objstore.Write{w}, audit.Event{
			Category: audit.CategoryAuthZ,
			Action:   "acl.remove",
			Actor:    actor,
			ActorSub: actorSub,
			Surface:  surface,
			Resource: fmt.Sprintf("acl/%s/%s/%s", row.Principal, row.Action, row.Scope),
			Folder:   row.Scope,
			Outcome:  audit.OutcomeOK,
			ParamsSummary: map[string]any{
				"principal": row.Principal,
				"action":    row.Action,
				"scope":     row.Scope,
				"effect":    row.Effect,
			},
		}, nil
	})
}

// PutACLRow is the audit-free twin of AddACLRow, for a caller with no audit
// context of its own.
func (s *Store) PutACLRow(row core.ACLRow) error {
	w, err := aclPut(aclDefaults(row))
	if err != nil {
		return err
	}
	// A refusal is the grant already being there (aclPut).
	_, err = s.TryWriteEntities(context.Background(), nil, w)
	return err
}

// aclItems reads one selection of the partition and turns it into rows. It is
// the ONE place a policy read materialises: an unreadable item aborts, never
// skips. A dropped DENY row with its allows intact is an escalation.
func (s *Store) aclItems(who string, q objstore.Query) ([]core.ACLRow, error) {
	items, err := s.Obj().Query(context.Background(), q)
	if err != nil {
		return nil, fmt.Errorf("acl: %s: %w", who, err)
	}
	out := make([]core.ACLRow, 0, len(items))
	for _, it := range items {
		r, err := aclRowFrom(it)
		if err != nil {
			return nil, fmt.Errorf("acl: %s: %w", who, err)
		}
		out = append(out, r)
	}
	return out, nil
}

// aclAll is the whole partition, in coordinate order.
func (s *Store) aclAll(who string) ([]core.ACLRow, error) {
	return s.aclItems(who, objstore.Query{Entity: ACL, Partition: OwnerRoutd})
}

// ACLRowsFor returns rows whose principal EXACTLY matches any element of
// principals; wildcard-bearing principals come from ACLWildcardRows. One
// begins_with per principal, and the separator ends it, so `folder:a` cannot
// reach `folder:ab`.
func (s *Store) ACLRowsFor(principals []string) ([]core.ACLRow, error) {
	if len(principals) == 0 {
		return nil, nil
	}
	var out []core.ACLRow
	for _, p := range principals {
		rows, err := s.aclItems("read rows for principals", objstore.Query{
			Entity: ACL, Partition: OwnerRoutd,
			Key: objstore.KeyPredicate{Op: objstore.PrefixKey, Value: p + KeySep},
		})
		if err != nil {
			return nil, err
		}
		out = append(out, rows...)
	}
	// Sorted, because the answer is assembled from several reads: an
	// evaluator's first-match answer must not depend on the order the caller
	// listed its principals in.
	aclOrder(out)
	return out, nil
}

// UserScopes lives in auth (auth.UserScopes): a projection of the rows
// auth.Authorize evaluates. A second read here missed wildcard-principal
// grants.

// ListACLByScope returns all acl rows whose scope exactly matches scope.
// Errors, never an empty list, on a read failure (file header).
func (s *Store) ListACLByScope(scope string) ([]core.ACLRow, error) {
	all, err := s.aclAll("read rows for scope " + scope)
	if err != nil {
		return nil, err
	}
	out := make([]core.ACLRow, 0, len(all))
	for _, r := range all {
		if r.Scope == scope {
			out = append(out, r)
		}
	}
	return out, nil
}

// ListACL returns every acl row, optionally filtered by principal.
func (s *Store) ListACL(principal string) ([]core.ACLRow, error) {
	if principal != "" {
		return s.ACLRowsFor([]string{principal})
	}
	return s.aclAll("list rows")
}

// ACLWildcardRows returns rows whose stored principal contains a glob segment
// (`*`); Authorize evaluates them as a second pass against the expanded
// principal set.
func (s *Store) ACLWildcardRows() ([]core.ACLRow, error) {
	all, err := s.aclAll("read wildcard rows")
	if err != nil {
		return nil, err
	}
	out := make([]core.ACLRow, 0, len(all))
	for _, r := range all {
		if strings.Contains(r.Principal, "*") {
			out = append(out, r)
		}
	}
	return out, nil
}

// aclOrder sorts rows the way the coordinate does.
func aclOrder(rows []core.ACLRow) {
	slices.SortFunc(rows, func(a, b core.ACLRow) int {
		return strings.Compare(
			strings.Join([]string{a.Principal, a.Action, a.Scope, a.Params, a.Predicate, a.Effect}, KeySep),
			strings.Join([]string{b.Principal, b.Action, b.Scope, b.Params, b.Predicate, b.Effect}, KeySep))
	})
}

// PutACLRowInto buffers a grant on the caller's write set, for routd's
// `/v1/acl` face: the grant rides the batch that carries resreg.invoke's one
// audit_log row, and AddACLRow would record the act twice. aclPut is a Merge,
// so it REFUSES NOTHING and can ride beside a write whose refusal is an answer.
func (s *Store) PutACLRowInto(ws *WriteSet, row core.ACLRow) error {
	w, err := aclPut(aclDefaults(row))
	if err != nil {
		return err
	}
	ws.Add(w)
	return nil
}

// RemoveACLRowInto buffers the revocation of ONE row on the caller's write set,
// with the error its refusal means: a revoke that did nothing must never read
// as success. All six coordinate fields address the row; a revoke that matched
// on fewer removed a wider grant than the caller named (BUGS L79).
func (s *Store) RemoveACLRowInto(ws *WriteSet, row core.ACLRow, refused error) error {
	key, err := aclKey(aclDefaults(row))
	if err != nil {
		return err
	}
	return ws.AddGuarded(refused, objstore.Write{
		Op: objstore.Delete, Entity: ACL, Key: key,
		Cond: objstore.Cond{Item: objstore.ItemPresent},
	})
}
