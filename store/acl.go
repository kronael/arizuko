package store

// acl.go holds every read and write of `acl`: one item per grant, addressed by
// the six fields that ARE the grant.
//
// THE PARTITION IS OwnerRoutd AND IT IS NOT A CHOICE THIS FILE MAKES. `acl` is
// a registered resreg resource, and an entity-backed resource's items live at
// its owner subsystem (resreg.SubsystemRoutd, which IS store.OwnerRoutd). A
// second spelling would put this file's items in one partition and the engine's
// read in another, and the failure is silent: a query on a partition nobody
// wrote to is a legal query that selects nothing.
//
// THE COORDINATE IS THE WHOLE GRANT — principal, action, scope, params,
// predicate, effect — because every one of the six changes what the row
// PERMITS. Two rows differing only in `effect` are an allow and a deny of the
// same call, and `params` and `predicate` each narrow a row to a subset of it.
// The principal LEADS, because that is what the evaluator selects on: the hot
// read is "every row for these principals", which is one begins_with per
// principal and never a scan.
//
// THE OTHER TWO READS ARE SCANS AND THAT IS MEASURED, not overlooked. A scope
// is the third field of the key and a wildcard principal is a substring of the
// first, and neither is a leading fragment — so ListACLByScope and
// ACLWildcardRows read the partition and filter in Go. The whole partition is
// 42 rows on krons, 23 on sloth and 16 on marinade, and the second of the two
// is on the authorize path, where it was already `LIKE '%*%'` — a table scan
// under another name.
//
// A READ FAILURE IS AN ERROR, NEVER AN EMPTY POLICY (BUGS A4). These readers
// once returned a bare nil on a query error and SKIPPED individual scan
// failures, so a broken table and a folder with no grants were the same value —
// the evaluator denied either way, but a management surface answered 200 with
// `[]`, and a partial scan could drop a DENY row while keeping the allows above
// it. One unreadable item fails the whole read.

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
// them: they are the key, and storing the coordinate twice is two facts that
// can disagree.
const (
	attrACLGrantedBy   = "granted_by"
	attrACLGrantedAt   = "granted_at"
	attrACLGrantOption = "grant_option"
)

// aclKeyFields is the coordinate, in order, for the error a refusal prints.
const aclKeyFields = "principal,action,scope,params,predicate,effect"

// aclKey is the coordinate of one grant.
//
// A value carrying the separator is REFUSED rather than joined: two different
// grants would address one item, the last write would win, and the read that
// splits the key back apart would answer a grant neither of them states. That
// is the same refusal resreg's own entity path makes over the same six fields
// (resreg/entity.go sortKeyFor), because both of them write these items.
//
// An EMPTY part is allowed — `params` and `predicate` are empty on almost every
// live row — because the separator count is fixed, so the parts still separate.
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

// aclRowFrom reads one item back. A key that does not split into six parts is a
// LOUD fault rather than a half-filled grant: half a coordinate names a row
// nobody wrote, and an evaluator reading it would answer about a call nobody
// granted.
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

// aclAttrs renders the three attributes a grant item carries.
//
// `grant_option` is ALWAYS written, as "0" or "1". A Go bool has no unset, so
// false is a value the row states rather than one it omits — and it is the
// exact text resreg's declared bool encoding renders (resreg/codec.go), because
// the two writers of these items have to agree byte for byte.
//
// `granted_at` is core.TS, at 30 bytes: that is the width resreg's entity path
// stamps a StampedField at, and a comparison over TEXT answers by byte order
// (BUGS L97). `arizuko migrate-routd` normalises the RFC3339 values the column
// path left behind.
func aclAttrs(r core.ACLRow) map[string]string {
	attrs := map[string]string{attrACLGrantOption: "0"}
	if r.GrantOption {
		attrs[attrACLGrantOption] = "1"
	}
	setAttr(attrs, attrACLGrantedBy, r.GrantedBy)
	setAttr(attrs, attrACLGrantedAt, r.GrantedAt)
	return attrs
}

// The two effect spellings an acl row may carry. This is the ONE declaration:
// the evaluator reads the same pair through auth, so a row can never hold a
// third value that one side reads and the other does not.
const (
	EffectAllow = "allow"
	EffectDeny  = "deny"
)

// HoldPrefix opens the namespace a hold rule's action lives in: `hold:mcp:reply`
// answers "must a call to `reply` wait for a human?", never "may it run". The
// declaration is here because the acl ROW is store's and the seed floor writes
// these actions; auth reads the same constant to evaluate them.
const HoldPrefix = "hold:mcp:"

// ShellEnvPrefix names the grant that puts a folder secret into the agent's
// shell. It lives here beside HoldPrefix, and auth aliases it, for the reason
// HoldPrefix does: the acl WRITE validates rows in this namespace and the
// evaluator reads them, so one declaration serves both and neither can drift
// into a prefix the other does not recognise.
const ShellEnvPrefix = "shell:env:"

// EffectAllowed reports whether `effect` is a spelling every evaluator reads.
func EffectAllowed(effect string) bool {
	return effect == EffectAllow || effect == EffectDeny
}

// aclPut renders the create-if-absent write both acl writers issue.
//
// Create-if-absent is what `INSERT OR IGNORE` said, and it matters: granting a
// row that is already there must not restamp who granted it or when, and must
// not silently clear a grant option the holder already has.
//
// THE EFFECT IS VALIDATED HERE, at the write, because every door into the acl
// entity renders through this function. The evaluator reads every effect that
// is not exactly EffectDeny as a grant, so a face that passes its argument
// straight through turns `denny` into an allow — a refusal an operator reads in
// the table and an evaluator never applies (BUGS L272). A guard in one face is
// a guard the next face forgets.
func aclPut(r core.ACLRow) (objstore.Write, error) {
	if !EffectAllowed(r.Effect) {
		return objstore.Write{}, fmt.Errorf(
			"acl: effect %q on action %q is not a spelling any evaluator reads; "+
				"use %q or %q", r.Effect, r.Action, EffectAllow, EffectDeny)
	}
	// A PREDICATE ON A shell:env: ROW CAN ONLY EVER MEAN "NEVER", so writing one
	// is refused here rather than discovered later. `ShellEnvKeys`
	// (auth/authorize.go) resolves once per dispatch keyed on the FOLDER and
	// builds its caller with no Claims, and `predicateMatches` answers true only
	// for an EMPTY predicate — so `predicate=platform=telegram`, which reads like
	// "only in Telegram", matches nothing on every channel and the key silently
	// stops appearing in the agent's shell. Nothing logs a denial because nothing
	// was denied (BUGS L134).
	//
	// Refused at the write for the same reason the effect is: every door into
	// this entity renders through this function, and a guard in one face is a
	// guard the next face forgets.
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
	// Every attribute is Init, which states the idempotence in the write: a
	// re-grant leaves the row exactly as it is rather than restamping who granted
	// it or when. It is a Merge rather than a guarded Put so it has NOTHING TO
	// REFUSE, which is what lets a folder creation's admin grant ride in the same
	// batch as the edge and the groups row it belongs with.
	return objstore.Write{
		Op: objstore.Merge, Entity: ACL, Key: key, Init: aclAttrs(r),
	}, nil
}

// ACLGrantWrite is aclPut for the daemons that assemble a folder creation's
// whole batch themselves, and it fills the defaults a caller may leave out, as
// every other door into aclPut does.
func ACLGrantWrite(row core.ACLRow) (objstore.Write, error) { return aclPut(aclDefaults(row)) }

// AdminGrantWrite renders the grant that makes `principal` the admin of a
// folder it has just created. It exists because the scope is the part a caller
// gets wrong: `matchSegments` (auth/acl.go) walks the glob segment by segment
// and ends on `len(in) == 0`, so a bare `eng` covers `eng` and REFUSES
// `eng/sre`, while `eng/**` covers both — `**` with nothing after it answers
// true whatever input is left. Two folder creators wrote two different scopes
// and the bare one left its creator unable to administer a child of their own
// folder (BUGS L206). One renderer, so there is no second shape to drift.
func AdminGrantWrite(principal, folder, grantedBy string) (objstore.Write, error) {
	return ACLGrantWrite(core.ACLRow{
		Principal: principal, Action: "admin", Scope: folder + "/**",
		Effect: EffectAllow, GrantedBy: grantedBy,
	})
}

// aclDefaults fills what a caller may leave out: the effect every grant has
// unless it says otherwise, and the instant it was granted.
func aclDefaults(r core.ACLRow) core.ACLRow {
	if r.Effect == "" {
		r.Effect = EffectAllow
	}
	if r.GrantedAt == "" {
		r.GrantedAt = core.NowTS()
	}
	return r
}

// AddACLRow grants a row idempotently and records it.
func (s *Store) AddACLRow(row core.ACLRow) error {
	row = aclDefaults(row)
	w, err := aclPut(row)
	if err != nil {
		return err
	}
	// A refusal is the grant already being there, which is what aclPut's
	// create-if-absent asks for: a re-grant must not restamp who granted it,
	// and it records nothing because nothing was granted.
	_, err = s.tryWriteAudited(func() ([]objstore.Write, audit.Event, error) {
		// GrantedBy is the grantor recorded in the row itself; it stands in as the
		// actor for callers that never set AsUser (the CLI, onbod invite-accept).
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
				// The grantor the row itself records. Distinguishes a grant an
				// operator typed from one a package install brought in, which the
				// actor alone cannot show — both are the same person at a terminal.
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

// PutACLRow grants a row idempotently WITHOUT emitting an audit_log row — the
// audit-free twin of AddACLRow, for a caller with no audit context of its own.
func (s *Store) PutACLRow(row core.ACLRow) error {
	w, err := aclPut(aclDefaults(row))
	if err != nil {
		return err
	}
	// A refusal is the grant already being there (aclPut).
	_, err = s.TryWriteEntities(context.Background(), nil, w)
	return err
}

// aclItems reads one selection of the partition and turns it into rows.
//
// It is the ONE place a policy read materialises, so the "a partial policy is
// never returned" rule holds for every reader at once: an unreadable item
// aborts, it does not skip. A dropped DENY row with its allows intact is an
// escalation, not a degradation.
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
// principals. Wildcard-bearing stored principals are fetched separately
// via ACLWildcardRows.
//
// One begins_with per principal, because the principal is the leading field of
// the coordinate: the separator ends it, so `folder:a` cannot reach
// `folder:ab`. That is the exactness `principal IN (…)` had.
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
	// Sorted, because the answer is assembled from several reads and each one
	// only orders inside itself. A policy read that returned a different order
	// per call would make an evaluator's first-match answer depend on the order
	// the caller happened to list its principals in.
	aclOrder(out)
	return out, nil
}

// UserScopes lives in auth (auth.UserScopes) — it is a projection of the rows
// auth.Authorize evaluates, not a query of its own. Reading `acl` here with a
// second read is what let it miss wildcard-principal grants.

// ListACLByScope returns all acl rows whose scope exactly matches scope.
// Errors, never an empty list, on a read failure — see the file header.
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
// Empty principal returns all rows.
func (s *Store) ListACL(principal string) ([]core.ACLRow, error) {
	if principal != "" {
		return s.ACLRowsFor([]string{principal})
	}
	return s.aclAll("list rows")
}

// ACLWildcardRows returns rows whose stored principal contains a glob
// segment (`*`). Evaluated by Authorize as a second pass against the
// expanded principal set.
//
// A substring is not a leading fragment of a key, so this reads the partition
// and filters — which is what `LIKE '%*%'` was, a table scan under a predicate
// no index answers.
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

// aclOrder sorts rows the way the coordinate does, for a caller holding a set
// it assembled from several reads.
func aclOrder(rows []core.ACLRow) {
	slices.SortFunc(rows, func(a, b core.ACLRow) int {
		return strings.Compare(
			strings.Join([]string{a.Principal, a.Action, a.Scope, a.Params, a.Predicate, a.Effect}, KeySep),
			strings.Join([]string{b.Principal, b.Action, b.Scope, b.Params, b.Predicate, b.Effect}, KeySep))
	})
}

// PutACLRowInto buffers a grant on the caller's write set, for routd's `/v1/acl`
// face: the grant rides the batch that carries resreg.invoke's single audit_log
// row, and AddACLRow would send a batch of its own and record the act twice.
//
// It renders through aclPut, so the row this writes carries the same coordinate
// and the same three attributes the audited writer does. A grant with no
// `granted_at` is stamped here rather than left off — the one omission an
// operator surface reads as "nobody granted this".
//
// aclPut is a Merge, so re-granting a row that is already there leaves it
// exactly as it is and REFUSES NOTHING — which is what lets the grant ride
// beside a write whose own refusal is an answer.
func (s *Store) PutACLRowInto(ws *WriteSet, row core.ACLRow) error {
	w, err := aclPut(aclDefaults(row))
	if err != nil {
		return err
	}
	ws.Add(w)
	return nil
}

// RemoveACLRowInto buffers the revocation of ONE row on the caller's write set,
// with the error its refusal means.
//
// The answer is load-bearing: a revoke is the one operation where "did nothing"
// must never read as success, so the delete is conditioned on the item being
// there and the refusal IS the not-found answer. The six coordinate fields
// address the row exactly, `params` and `predicate` included — a revoke that
// matched on fewer would remove a wider grant than the caller named (BUGS L79).
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
