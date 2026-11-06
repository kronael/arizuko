---
status: draft
depends:
  [
    ../5/32-acl-unified,
    ../5/33-paths-roles,
    ../5/19-hitl-firewall,
    ../5/31-identity-pairing,
    26-personal-mode,
    40-store-surface,
  ]
---

# specs/6/52 — acl layers: one evaluator, the release's rules under the operator's, one gate on every grant

Access control is one rule shape read by one evaluator. The rules come from two
layers: the release's defaults, which are code in one file, and the local
rows in `acl`, which the operator and the agents write. For one call, the
local rows decide when any of them matches, and the defaults decide only
when none does. Every verdict reads one consistent state of the rules, and
every write to them passes one containment check: a writer never grants more
than it holds.

The owner's words, 2026-11-04 and 2026-11-05: "like a config a distro ships
… you do have a way to override", "cant rows be layered on top of another?",
"defaults in code ok but in a single place, well readable", "only allow
always for concrete rules … or you have to use some operator prompt", and on
delegation "you need to check the grant he is granting and reject that
also".

## The problem — two evaluators, defaults that never arrive, seven write doors with five rules

Read at `684b3eeed`.

- **Two evaluators over one row.** `Authorize` folds deny-wins over the
  lattice (`auth/authorize.go:88-122`). `CheckHold` and `ShellEnvKeys` fold
  the same rows again in `namespacedAllows`, with exact action matching
  (`auth/authorize.go:619-646`). Three gates decide from a listing instead:
  `auth.IsOperator` reads `**` in the stamped `X-User-Groups`
  (`auth/dashauth.go:54-56`, BUGS `L659` names its readers), routd's
  `IsOperator` takes membership in `role:operator` as the answer before any
  deny is read (`routd/sibling_db.go:269-271`), and dashd shows a folder's
  pages when that listing holds a scope covering it (`dashd/authz.go:55-66`),
  then folds the listing's scope strings in as principals
  (`dashd/authz.go:198-205`).
- **The defaults are rows written once.** `SeedRoutdFloor` writes the 39 floor
  rows only into an empty `acl` (`store/seed.go:76-111`). An instance born
  before a floor row existed never receives it: the 23 skip rows went into
  krons and sloth by hand on 2026-10-20, and the two `service:dashd` rows of
  2026-10-27 are on neither (BUGS `L473`).
- **A default cannot yield.** The skip rows are `deny` on `role:member` at
  `**`, and deny wins over every hold row (`auth/authorize.go:639-645`). An
  operator row that holds `reply` in one folder never holds it (BUGS `L273`).
- **Seven write doors, five rules.** The agent socket and REST call
  `auth.Delegate` for an add by a non-root writer only
  (`routd/acl_resource.go:67`, `:159`); a remove passes unchecked. dashd checks
  an allow and lets any deny through (`dashd/grants_admin.go:229-237`), and a
  deny on a `hold:` action is a skip, which widens what the agent does
  unwatched. `/always` runs its own check, that the reviewer holds `admin`
  (`routd/pending_actions.go:347`, `:371`). `invite_create` checks only that the
  agent may call the tool, and the redemption writes `admin` (BUGS `L664`).
  `unpair` removes an edge whose parent may hold a deny (BUGS `L665`). The
  host CLI checks nothing (`cmd/arizuko/main.go:627-651`).
- **`Delegate` covers more than the granter holds.** A held `a/???` covers the
  requested glob `a/[x]`, which then matches `a/x`; a deny on `a/*/x` does not
  block a grant on `a/b/*`, which meets it at `a/b/x` (BUGS `L667`, measured).
- **A verdict reads the rules in pieces.** `Authorize` reads each principal's
  rows, each membership level and the route's ancestors with separate queries
  (`auth/authorize.go:267-282`, `:289-316`, `:129-135`). A revoke of a direct
  allow and the removal of an inherited deny, landing between two of those
  reads, give a verdict no single state gives. `Delegate`'s answer is not tied
  to the write it permits: the rows it proved can change before the grant
  lands.
- **Two operator overloads that disagree.** routd writes the `role:operator`
  edge for scope `**` with the action omitted or `*`
  (`routd/acl_resource.go:234-239`); the CLI writes it for `admin` at `**`
  after it fills the omitted action with `admin`
  (`cmd/arizuko/main.go:617-620`). `add_acl(action="admin", scope="**")` is a
  row on one door and the edge on the other.
- **A failed read hides a deny.** `Ancestors` returns nothing when a
  membership read fails (`store/membership.go:376-378`), so a deny on a role
  the caller holds is not read and a direct allow stands. `CheckHold` holds
  nothing when the `acl` read fails (`auth/authorize.go:593-605`, BUGS `L92`).
- **`/always` widens to a whole tool.** A tool with no declared keys gets a
  tool-wide skip row (`routd/pending_actions.go:516-517`, `:561-571`).

## Where it fits

```
 auth/defaults.go (new)                      acl rows (unchanged): the operator,
 the release's rules, one file               /always, onbod, folder creation
        │ default layer (new)                       │ local layer (unchanged)
        ▼                                           ▼
 ┌─────────────────────────────────────────────────────────────────────────┐
 │ auth.ReadPolicy (new): one objstore.ReadConsistent over routd's scope    │
 │ Policy.Allows (new):                                                    │
 │   any matching local row → the local rows decide, a deny wins           │
 │   else                   → the default rows decide, a deny wins         │
 └───┬────────────┬─────────────┬─────────────┬──────────────┬─────────────┘
     │ Authorize  │ CheckHold   │ ShellEnvKeys│ UserScopes   │ CheckGrant (new)
     │ (changed)  │ (changed)   │ (changed)   │ (changed)    │ + the version it read
     ▼            ▼             ▼             ▼              ▼
  every gate,  holdGate      spawn env     proxyd's       acl rows, edges, unpair,
  IsOperator,                              X-User-Groups  dashd grants, /always,
  dashd pages                                             invites (all changed)
  (changed)                                                      │ write + BumpAt (new)
                                                                 ▼
                                                           GuardedBatch (unchanged)
 auth.namespacedAllows (removed)   auth.Delegate (removed)
 store.SeedRoutdFloor's acl rows (removed)   tool-wide /always (removed)
 dashd's listing as visibility (removed)
```

```yaml
- path: auth/defaults.go (new)
  job: the release's rules, read top to bottom; the only place they are written
  names:
    - 'memberVerbs []string'
    - 'skipApproval []struct{ tool, why string }'
    - 'defaultRows []core.ACLRow'
  never: reads the store, or carries a row the evaluator could not store
- path: auth/authorize.go (changed)
  job: one consistent policy read, one fold; Authorize, CheckHold,
    ShellEnvKeys, EffectiveActions and UserScopes are views of it
  names:
    - 'Policy struct{ caller Caller; route []string; local, defaults []core.ACLRow }'
    - 'ReadPolicy(s *store.Store, caller Caller) (Policy, objstore.Version, error)'
    - '(Policy) Allows(action, scope string, params map[string]string) bool'
    - 'CheckHold(s, caller, tool, scope, params) (bool, error)'
  never: folds a row a second way, answers from a listing, or reads outside
    ReadPolicy
- path: auth/grant.go (new, replaces auth/delegate.go)
  job: the one containment gate every policy write passes
  names:
    - 'Mutation struct{ Remove bool; Row core.ACLRow; Role string }'
    - 'CheckGrant(s *store.Store, granter Caller, m Mutation) (objstore.Version, error)'
    - 'scopeCovers(held, want string) bool'
    - 'mayMeet(a, b string) bool'
  never: writes, or exempts a writer by who it is
- path: objstore/consistent.go, objstore/marker.go (changed)
  job: the consistent read returns the version it read at; a Bump can carry it
  names:
    - 'type Version string'
    - 'ReadConsistent(ctx, s, sc, read) (Version, error)'
    - 'BumpAt(sc Scope, v Version) Write'
  never: adds a method to Store or a field to Write
- path: store/writeset.go, store/scopes.go (changed)
  job: a proved set renders routd's Bump as BumpAt; a refusal says whether the
    rules moved or the set's guarded write refused
  names:
    - '(*WriteSet).Prove(v objstore.Version)'
    - 'ErrPolicyMoved'
- path: resreg/archive.go (changed)
  job: reads ReadConsistent's version and ignores it, as today
- path: store/seed.go (changed)
  job: seeds the base egress allowlist into an empty network_rules
  never: writes an acl row
- path: store/membership.go (changed)
  job: names role:world; reports a failed membership read
  names:
    - 'RoleWorld = "role:world"'
    - 'Ancestors(child string) ([]string, error)'
- path: store/acl.go (changed)
  job: the one test for the operator overload
  names:
    - 'OperatorShape(raw core.ACLRow) bool'
- path: onbod/main.go, store/invites.go (changed)
  job: writeWorld writes the role:world edge where it wrote the hold:mcp:* row;
    a redemption passes CheckGrant with the invite's issuer as granter
  names:
    - 'InviteGrantRow(targetGlob string) core.ACLRow'
  never: renders the redemption's row a second way at issue
- path: routd/acl_resource.go, dashd/grants_admin.go, routd/pending_actions.go,
    routd/membership_resource.go, routd/mcp.go, routd/steer.go, dashd/invites.go (changed)
  job: every add and every remove, every edge removal and every invite issue
    calls CheckGrant and proves its write with the version; /always is offered
    only for a tool with keys; holdGate blocks a call whose CheckHold returns an
    error; /invite records its sender as the issuer
  never: skips CheckGrant for root
- path: routd/sibling_db.go (changed)
  job: IsOperator asks Authorize
  never: decides from membership presence
- path: dashd/authz.go (changed)
  job: a page is visible when the caller's policy allows interact on its folder
  never: reads X-User-Groups, or makes a scope string a principal
```

`objstore`'s two methods do not change, and neither does the `Write` it
carries: `BumpAt` is `Bump` with a `Cond` the type already has, the
compare-and-swap the versioned replace writes on the same marker
(`objstore/replace.go:189`). The `acl` row, its six-field coordinate
(`store/acl.go:37-57`) and `acl_membership` do not change.

## A rule, and the two layers it comes from

A rule is the row `specs/5/32-acl-unified.md` owns, unchanged: `(principal, action,
scope, params, predicate, effect, grant_option)`, `core.ACLRow`. The layer is
not a field. Every row in `acl` is local; every row in `auth/defaults.go` is a
default. A writer cannot make a default, so there is no attribute to forge,
and `granted_by` stays an audit field the evaluator never reads.

Defaults reach every instance with the binary: nothing is written at a start,
so an instance born before a default existed reads it at the next deploy, and
a local row an operator wrote survives every restart. Revoking the local row
that overrides a default brings the current default back.

A package cannot declare `acl` rows (`EXTENDING.md` § Package definition), so
the release is the only source of defaults, and two sources never meet on one
rule. The package order rule (`EXTENDING.md` § Order and overrides) is the
one to apply when a package may ship rules; that case does not exist today.

## One evaluator, and the layer it reads

```
 call (caller, action, target, claims, params)
   │ ReadPolicy (new): rows of the caller's expanded principals, both layers,
   │ inside one ReadConsistent; a failed acl or membership read, or a scope a
   │ replace holds → error → the call is refused
   ▼
 matching (changed): action (namespaces isolated), scope, predicate, params;
                     an identity-axis allow only inside the route's subtree
   ├─ some local row matches → the local rows decide
   └─ none                   → the default rows decide
   ▼
 a matching deny → DENY;  a matching allow → ALLOW;  nothing → DENY
```

Current, `auth/authorize.go:88-122` (the `Authorize` fold) and `:619-646`
(the second fold):

```go
	allowed, denied := false, false
	for _, r := range rows {
		if !matchPattern(r.Scope, scope) {
			continue
		}
		if !predicateMatches(r.Predicate, caller.Claims) {
			continue
		}
		if !paramsMatch(r.Params, params) {
			continue
		}
		if r.Effect == "deny" {
			if actionCovers(r.Action, action) {
				denied = true
			}
			continue
		}
		if caller.Route == "" || anyPrincipalMatches(r.Principal, route) {
			if actionCovers(r.Action, action) {
				allowed = true
			}
			continue
		}
		if withinSubtree(caller.Route, scope) && identityCovers(r.Action, action) {
			allowed = true
		}
	}
	if denied {
		return false
	}
	return allowed

func namespacedAllows(
	rows []core.ACLRow,
	prefix, name, scope string,
	claims, params map[string]string,
) bool {
	var allowed, denied bool
	want := prefix + name
	for _, r := range rows {
		if r.Action != want && r.Action != prefix+"*" {
			continue
		}
		// ... the same scope, predicate and params tests
		if r.Effect == EffectSkipApproval {
			denied = true
		} else {
			allowed = true
		}
	}
	return allowed && !denied
}
```

Proposed:

```go
// Allows is the one decision. A local row that matches is the operator's word
// for this call, so the local rows decide whenever one matches; the release's
// defaults answer only the calls no local row speaks to. Inside the deciding
// layer a deny wins, and no matching allow is a deny.
func (p Policy) Allows(action, scope string, params map[string]string) bool {
	q := query{action: action, scope: scope, params: params}
	if rows := p.matching(p.local, q); len(rows) > 0 {
		return verdict(rows)
	}
	return verdict(p.matching(p.defaults, q))
}

// verdict: any deny refuses, any allow admits, nothing refuses.
func verdict(rows []core.ACLRow) bool {
	for _, r := range rows {
		if r.Effect == store.EffectDeny {
			return false
		}
	}
	return len(rows) > 0
}

// actionCovers keeps the lattice for every namespace but two. A `hold:` or
// `shell:env:` action is matched exactly or by its own `*`: an operator's
// (`*`, `**`) row would otherwise hold every call it makes and put every
// folder secret in its turns (spec 5/19 Hazard 2).
func actionCovers(rowAction, requested string) bool {
	if rowAction == requested {
		return true
	}
	for _, ns := range []string{HoldPrefix, ShellEnvPrefix} {
		if strings.HasPrefix(requested, ns) {
			return rowAction == ns+"*"
		}
	}
	switch rowAction {
	case "*":
		return true
	case "admin":
		return requested == "interact" || strings.HasPrefix(requested, "mcp:")
	case "mcp:*":
		return strings.HasPrefix(requested, "mcp:")
	}
	return false
}
```

`matching` is the body of today's loop: the scope, predicate and params tests,
`actionCovers` for a deny and for a route-axis allow, and `withinSubtree` with
`identityCovers` for an identity-axis allow (`auth/authorize.go:105-114`). A
deny is never confined. `Authorize` is `ReadPolicy` then `Allows`.
`CheckHold(folder, tool)` is `Allows(HoldPrefix+tool, f)` on the policy of
`folder:<f>`, and `ShellEnvKeys` is the same per key on `ShellEnvPrefix`. No
call to `Authorize` asks a `hold:` or `shell:env:` action today, so the
isolation changes no `Authorize` verdict. `EffectiveActions` and `UserScopes`
list the allows of both layers, so a member verb that leaves the store stays
listed.

The layer split changes no verdict outside `hold:`. The defaults hold no deny
in any other namespace, so when a local row matches, the defaults it hides
could only add an allow to a verdict the local rows already gave: a local
allow admits either way, and a local deny refuses either way. The split does
change what a principal may delegate, so `CheckGrant` reads the deciding layer
too (§"The containment tests").

A read failure refuses. `ReadPolicy` returns the error of the `acl` read and
of each membership read (`Ancestors` returns its error in place of an empty
set), `Authorize` denies as today, and `CheckHold` returns the error, which
`holdGate` renders as a blocked call the way it renders a hold it could not
record (`routd/pending_actions.go:418-425`, BUGS `J3`). BUGS `L92` closes in
that direction: a broken `acl` blocks a held-eligible call instead of
releasing it.

### One read of one state

Current, `auth/authorize.go:70-85`: the rows of the expanded set, read with
`Query` per principal after a membership walk of its own, and the route's
ancestors read again at `:129-135`:

```go
	expanded := expandPrincipals(s, caller)
	rows, err := policyRows(s, expanded)
	if err != nil {
		slog.Error("authorize: acl read failed; DENYING", /* ... */)
		return false
	}
	route := routePrincipals(s, caller)
```

Proposed:

```go
// ReadPolicy is the one read behind every verdict: the caller's principals,
// both axes, and their rows, inside one consistent read of routd's scope, so
// a verdict is a state the rules held at one instant. acl and acl_membership
// both move that scope (store/scopes.go:60-82). The version is what a write
// resting on this read proves itself with (§"The proof").
func ReadPolicy(s *store.Store, caller Caller) (Policy, objstore.Version, error) {
	var p Policy
	v, err := objstore.ReadConsistent(context.Background(), s.Obj(),
		store.OwnerScope(store.OwnerRoutd), func(context.Context) error {
			p = Policy{caller: caller}
			expanded, route, err := expandPrincipals(s, caller)
			if err != nil {
				return err
			}
			p.route = route
			p.local, err = policyRows(s, expanded)
			p.defaults = defaultsFor(expanded)
			return err
		})
	return p, v, err
}
```

`ReadConsistent` exists and the export already reads through it
(`objstore/consistent.go:24-42`, `resreg/archive.go:72`). It costs two point
reads of the marker per verdict, and a retry when a routd-owned write lands
inside the read. The caveat: while a replace holds routd's scope, which is
`arizuko apply` to routd, every verdict is refused. The rows are part-replaced
then, so no verdict read from them is one the rules gave.

## The hold decision, through the same evaluator

```
 tools/call (tool, args) on the turn of folder f
   │ ipc: holdEligible (unchanged)
   ▼
 CheckHold (changed) = Allows(hold:mcp:<tool>, f, args) on folder:f
   ├─ error → blocked, the J3 notice (changed)
   ├─ false → the tool's own gate: Authorize (unchanged)
   └─ true  → pending row, notice, the waiter (unchanged)
                └─ approved → the tool's own gate: Authorize (unchanged)
```

Current, `auth/authorize.go:589-607`:

```go
func CheckHold(s *store.Store, caller Caller, tool, scope string, params map[string]string) bool {
	if s == nil || caller.Principal == "" || tool == "" {
		return false
	}
	rows, err := policyRows(s, expandPrincipals(s, caller))
	if err != nil {
		slog.Error("check hold: acl read failed; holding NOTHING",
			"principal", caller.Principal, "tool", tool, "err", err)
		return false
	}
	return namespacedAllows(rows, HoldPrefix, tool, scope, caller.Claims, params)
}
```

Proposed:

```go
// CheckHold answers "must this call wait for a human?" with the one evaluator,
// on the hold namespace. An allow holds the call; a deny is "skip approval"
// and holds nothing. Neither grants the tool: the tool's own gate still runs.
func CheckHold(s *store.Store, caller Caller, tool, scope string, params map[string]string) (bool, error) {
	if s == nil || caller.Principal == "" || tool == "" {
		return false, nil
	}
	p, _, err := ReadPolicy(s, caller)
	if err != nil {
		return false, fmt.Errorf("check hold: %w", err)
	}
	return p.Allows(HoldPrefix+tool, scope, params), nil
}
```

The hold is asked of the folder principal, single-axis, as today
(`routd/pending_actions.go:410`): whether a call waits is the folder's policy,
not the sender's (`specs/5/33-paths-roles.md` §"Two axes"). The approval is
bound to one call: the waiter suspended on that call runs it once, and a
second call is a second pending row. Approving needs `admin` on the held
row's folder through `Authorize` (`routd/pending_actions.go:347`), and the
tool's own gate runs after the approval, so an approval never defeats a deny.

The hold is asked before the tool's own gate, as today, and not after it. The
gate's action and target are resolved inside each tool's handler; moving them
in front of the hold would put a second resolver of every tool's target in
ipc. The caveat: a human can be asked about a call the gate then refuses. The
call still does not run.

## The defaults are one file, read top to bottom

```
 auth/defaults.go (new) ── defaultRows ──► ReadPolicy (the only reader)
 store/seed.go: memberVerbs, alwaysMountedTools, skipApprovalFloor and the
   acl half of SeedRoutdFloor (removed)
 onbod writeWorld: folder:w → role:world edge (new),
   folder:w hold:mcp:* row (removed)
```

Current, `store/seed.go:29-58` (the lists) and `:76-111` (the write):

```go
var memberVerbs = []string{
	"mcp:reply", "mcp:send", "mcp:send_file", "mcp:send_voice",
	"mcp:post", "mcp:forward", "mcp:quote", "mcp:repost",
	"mcp:like", "mcp:dislike", "mcp:edit", "mcp:delete",
	"mcp:disengage",
}

var alwaysMountedTools = []string{
	"mcp:inspect_routing", "mcp:inspect_tasks", "mcp:inspect_session",
	"mcp:inspect_identity", "mcp:inspect_messages", "mcp:find_messages",
	"mcp:get_thread", "mcp:fetch_history", "mcp:get_work", "mcp:set_work",
}

func skipApprovalFloor() []string { /* memberVerbs + alwaysMountedTools */ }

func SeedRoutdFloor(ctx context.Context, d objstore.Store) error {
	empty, err := entityIsEmpty(ctx, d, ACL)
	// ...
	if empty {
		rows := []core.ACLRow{{
			Principal: RoleOperator, Action: "*", Scope: "**", Effect: "allow",
			GrantedBy: "system", GrantedAt: core.TS(operatorGrantedAt), GrantOption: true,
		}}
		// + service:dashd chats:get and chats:update at **
		// + role:member <memberVerbs> at ** allow
		// + role:member hold:mcp:<skipApprovalFloor> at ** deny
		// each written with putIfAbsent
	}
	// ... the network_rules half
}
```

Proposed, `auth/defaults.go`, the whole file:

```go
package auth

// defaults.go is the release's access policy, the ONLY place it is written.
// The evaluator reads these rows under the local rows in `acl`: for one call,
// a local row that matches decides, and these decide only when none does. An
// operator overrides a default with a local row and gets it back by revoking
// that row. The docs name this file and never copy it.

import (
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/store"
)

// memberVerbs are what every folder may do: answer the chats it is routed to.
// Scope ** because the route, not the scope, bounds where a message goes.
var memberVerbs = []string{
	"reply", "send", "send_file", "send_voice", "post", "forward", "quote",
	"repost", "like", "dislike", "edit", "delete", "disengage",
}

// skipApproval: the calls a world's agent makes without asking, one row per
// tool, and why a person need not see the call. A write that every turn
// mounts (invite_create, pin_message, reset_session) is NOT here: it waits.
var skipApproval = []struct{ tool, why string }{
	{"reply", "answers the chat the turn is in"},
	{"send", "a message to a chat the folder's routes already reach"},
	{"send_file", "send, with a file"},
	{"send_voice", "send, as speech"},
	{"post", "a post on an account the folder is routed to"},
	{"forward", "passes a message to a chat the folder already reaches"},
	{"quote", "a reply that quotes a message"},
	{"repost", "a repost on an account the folder is routed to"},
	{"like", "a reaction; it changes no content"},
	{"dislike", "a reaction; it changes no content"},
	{"edit", "the platforms let a bot edit only its own messages"},
	{"delete", "removes a message inside the platform's own bounds"},
	{"disengage", "stops the agent answering; it acts on nothing else"},
	{"inspect_routing", "reads the folder's routes"},
	{"inspect_tasks", "reads the folder's scheduled tasks"},
	{"inspect_session", "reads the turn's own session"},
	{"inspect_identity", "reads who is speaking"},
	{"inspect_messages", "reads the chat's messages"},
	{"find_messages", "searches the chat's messages"},
	{"get_thread", "reads one thread"},
	{"fetch_history", "reads the platform's history of the chat"},
	{"get_work", "reads the folder's work notes"},
	{"set_work", "writes the folder's own work notes, its memory checkpoint"},
}

// defaultRows is the default layer. Every row is on a role or a service, so
// it reaches exactly the principals the operator puts in that role, and a
// folder taken out of a role loses its rows.
var defaultRows = func() []core.ACLRow {
	rows := []core.ACLRow{
		// The delegation root: an operator holds everything, with the option.
		{Principal: store.RoleOperator, Action: "*", Scope: "**",
			Effect: store.EffectAllow, GrantOption: true},
		// dashd reads and writes chat settings on every folder.
		{Principal: "service:dashd", Action: "chats:get", Scope: "**", Effect: store.EffectAllow},
		{Principal: "service:dashd", Action: "chats:update", Scope: "**", Effect: store.EffectAllow},
		// A world holds every call for a person to see before it runs.
		{Principal: store.RoleWorld, Action: HoldPrefix + "*", Scope: "**",
			Effect: store.EffectAllow},
	}
	for _, v := range memberVerbs {
		rows = append(rows, core.ACLRow{Principal: store.RoleMember,
			Action: "mcp:" + v, Scope: "**", Effect: store.EffectAllow})
	}
	for _, e := range skipApproval {
		rows = append(rows, core.ACLRow{Principal: store.RoleMember,
			Action: HoldPrefix + e.tool, Scope: "**", Effect: store.EffectDeny})
	}
	return rows
}()
```

The 40 rows are the 39 the seed writes at `684b3eeed` plus the world's
blanket hold, the tool names spelled as production passes them: the hold gate
passes `reply`, never `mcp:reply` (`auth/checkhold_test.go:15-31` passes the
wrong spelling and is corrected). `memberVerbs` and `skipApproval` share 13
names and neither derives from the other: one answers what a member may call,
the other which of its calls skip a human.

The world's blanket hold is a default on `role:world` because a stored
`hold:mcp:*` row on the world matches every tool, so the local layer would
decide every hold of the world and the skip rows in the default layer would
never be read. A role is the codebase's way to say "this folder is of this
kind" (`GRANTS.md:18-24`), and membership is read at every call:
a world taken out of `role:world` stops holding, and one taken out of
`role:member` holds its messaging too. The blanket's scope is `**` where
writeWorld wrote `w/**`; the hold is asked about the turn's own folder
(`routd/sibling_db.go:258-260`), so both match the same calls.

## CheckGrant — one containment gate for every write

```
 add_acl, remove_acl (changed)      ─┐
 POST, DELETE /v1/acl (changed)     ─┤
 dashd grants add, revoke (changed) ─┤ the door's gate (unchanged): may the
 /always (changed)                  ─┤ writer write rules on this target?
 unpair, REST and agent (changed)   ─┤                │
 invite issue, redemption (changed) ─┘                ▼
                        auth.CheckGrant (new): ReadPolicy of the granter and
                        of every principal the write touches → version v;
                        what does this write move, and does the granter hold it?
                          ├─ no  → 403, nothing written
                          └─ yes → v
                                                      │
                                                      ▼
                        GuardedBatch: the write, its audit row, BumpAt(v) (new);
                        it commits only while routd's scope is still at v
 auth.Delegate (removed)    the root exemptions (removed)
 host CLI: arizuko group grant, arizuko apply (unchanged, the trust root)
```

Current, `auth/delegate.go:34-67`:

```go
func Delegate(s *store.Store, granter string, want []core.ACLRow) error {
	held, err := grantableRows(s, granter)
	// ...
	denies, err := denyRows(s, granter)
	// ...
	for _, r := range want {
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
		if blockedByDeny(denies, r) {
			return fmt.Errorf("cannot delegate %s on %s: granter holds a deny covering it",
				r.Action, r.Scope)
		}
	}
	return nil
}
```

Current, the root exemption, `routd/acl_resource.go:159-169`:

```go
		if x.Action == resreg.Action("add") && !callerID.IsRoot {
			want := store.GrantDefaults(aclArgsRow(x.Args))
			if err := auth.Delegate(s.db.store(), callerSub, []core.ACLRow{want}); err != nil {
				return resreg.Errorf(http.StatusForbidden, "%v", err)
			}
		}
```

Proposed:

```go
// Mutation is one write to the rules: Row added or removed, or, when Role is
// set, the membership edge Row.Principal → Role added or removed.
type Mutation struct {
	Remove bool
	Row    core.ACLRow
	Role   string
}

// CheckGrant refuses a write that hands out authority the granter does not
// hold. granter is the Caller the door's gate evaluates; on a turn that is the
// route and the attested sender, and the sender's denies count. It returns the
// version of the one read it decided on, which the write's batch proves.
func CheckGrant(s *store.Store, granter Caller, m Mutation) (objstore.Version, error)
```

```go
	// every door, add and remove alike, root included
	v, err := auth.CheckGrant(s.db.store(), granter, auth.Mutation{Remove: remove, Row: row})
	if err != nil {
		return resreg.Errorf(http.StatusForbidden, "%v", err)
	}
	x.Writes.Prove(v)
```

What one write moves, and when it is contained:

| write                                                            | the authority it moves                                            | contained when                                                                                   |
| ---------------------------------------------------------------- | ----------------------------------------------------------------- | ------------------------------------------------------------------------------------------------ |
| add an allow, or remove a deny, outside `hold:` and `shell:env:` | the row's action on its scope, with its params                    | the granter's deciding layer holds it with the option, and no granter deny may meet it           |
| add a deny, or remove an allow, outside `hold:`                  | none: it narrows                                                  | always; the door gate decided who may write there                                                |
| remove any local row                                             | each default row of the row's principal that may meet it          | each, narrowed to the removed row's action and scope, is contained as an add                     |
| add or remove any `hold:` row                                    | an approval standing for the calls it matches                     | the granter holds `admin` on every target in the row's scope                                     |
| add a `shell:env:` allow, or remove its deny                     | a folder secret into an agent's shell                             | a granter allow WITH the option on the whole scope whose action is `*`, `shell:env:*` or the key |
| add the edge child → role R                                      | every allow and `hold:` row R holds, both layers                  | each row is contained by the rules above                                                         |
| remove the edge child → R, a pairing edge included               | every deny and `hold:` row R holds, both layers                   | each row is contained by the rules above                                                         |
| issue or redeem an invite                                        | `admin` on the target, or on `<target>*/**` for a subgroup invite | as an added allow (§"Invitations and unpairing are grant writes")                                |

### The containment tests

Each test is conservative, and an inclusion it cannot prove is a refusal.

- **Covers** is today's `coveredByGrantable` with a stricter scope test:
  `actionCovers` with the two namespaces isolated, `scopeCovers` and
  `paramsCover` (`auth/delegate.go:178-180`). A granter allow with a
  predicate covers nothing, because it holds only under a claim. Current,
  `auth/delegate.go:160-173`:

  ```go
  func scopeCovers(held, want string) bool {
  	if held == want || held == "**" {
  		return true
  	}
  	if base, ok := strings.CutSuffix(held, "/**"); ok {
  		return groupfolder.Contains(base, want)
  	}
  	if strings.ContainsAny(want, "*") {
  		return false
  	}
  	return matchPattern(held, want)
  }
  ```

  Proposed: the last test refuses every glob character `path.Match` reads,
  so a held `a/???` no longer covers the glob `a/[x]` (BUGS `L667`):

  ```go
  	// held has no recursive tail, so it covers only a want with no glob
  	// character: `?`, `[` and `\` widen a want as `*` does.
  	if strings.ContainsAny(want, `*?[\`) {
  		return false
  	}
  	return matchPattern(held, want)
  ```

- **May meet** replaces "one scope covers the other" (`auth/delegate.go:122`).
  It answers "cannot meet" only on proof:

  ```go
  // mayMeet reports whether two scope globs can match one path. It answers
  // false only on proof: before the first `**` of either glob, segments align
  // one to one, so two segments without a glob character that differ there
  // keep the globs apart; and two globs without `**` match paths of their own
  // length only. A `**` makes every later alignment possible.
  func mayMeet(a, b string) bool
  ```

  `a/*/x` and `a/b/*` meet at `a/b/x`, so the grant of the example in
  §"The problem" is refused. `a/**/x` and `a/b/c/*` compare only their first
  segment, which is equal, so they may meet, and they do at `a/b/c/x`. A
  deny's params and predicate are not read: a deny that might apply counts.

- **The axes.** The covering allow comes from the route axis, or from the
  caller's own principal on a single-axis caller: a sender's allows never
  count toward what a turn may grant, since execution confines them to the
  route's subtree. The denies come from every principal of the caller, both
  axes, as execution reads them (`auth/authorize.go:57-58`). A folder that
  holds `X` with the option cannot grant `X` during a turn whose sender is
  denied `X`, and cannot remove that sender's deny.
- **The deciding layer.** Delegation reads the layer that decides, as
  `Allows` does. When some local row of the granter may meet the wanted
  authority, the local layer decides there, and the write is contained only
  when a local allow with the option covers it and no local deny may meet
  it. When no local row may meet it, the same test runs on the default rows. A folder in `role:operator` with a local
  `mcp:X a allow` without the option therefore cannot grant `mcp:X` on `a`
  through the default `*` on `**`: the local row decides that call, and it
  holds no option.
- **What a removal reveals.** Removing a local row of principal P lets the
  default layer decide where that row matched. Each default row of P's
  principals that may meet the removed row is checked as an add, narrowed to
  the removed row's action and scope, with that default's effect and option.
  Removing a local allow without the option that hides `role:operator`'s
  default is checked as adding that allow with the option. Removing a local
  deny is also checked as granting its allow, as the table says, because it
  reveals the local allows it blocked.
- **A wildcard principal** on an added allow is refused, as today
  (`auth/delegate.go:54-56`): the rows it reaches are not known at write time.

### The proof

CheckGrant reads every row it decides on, the granter's and, for an edge or a
removal, those of the role or the principal it changes, inside one
`ReadPolicy`, so the rules it judges are one state at version `v`. The write's
batch carries routd's Bump at that version, so it commits only while no write
to routd's scope has landed since. A revoke, a new deny, a grant-option change
on the same coordinate and a new membership edge all move it, because `acl`
and `acl_membership` both move routd's marker (`store/scopes.go:60-81`). The
Bump is a write the batch already makes, so no item is named twice
(`objstore/batch.go:108-120`).

Current, `objstore/marker.go:88-93`:

```go
func Bump(sc Scope) Write {
	return Write{
		Op: Update, Entity: sc.Entity, Key: sc.Key,
		Attrs: map[string]string{markerVersion: freshVersion()},
	}
}
```

Proposed, beside it:

```go
// BumpAt is Bump for a write that rests on a consistent read: it moves the
// marker only while the marker is still at v, so the batch commits only on
// the state the read saw.
func BumpAt(sc Scope, v Version) Write {
	w := Bump(sc)
	w.Cond = Cond{Where: []Cmp{{Attr: markerVersion, Op: Eq, Value: string(v)}}}
	return w
}
```

A batch that also carries a guarded write, a revoke's "not found" or
`/always`'s verdict, still reports one bit. On a refusal `Apply` reads the
marker once: moved, the answer is `ErrPolicyMoved`, a 409 the caller sends
again; unchanged, it is the guarded write's own error, because an unchanged
version means no other write landed. The caveat: every routd-owned write
moves the same version, routes, tasks and pending actions among them, so a
grant that lands beside one is refused and sent again. A grant is a write at
the pace of a person.

### Who the granter is

The granter is the Caller the door's gate evaluates: the JWT sub on REST and
dashd, the reviewer on `/always`, on the agent socket the turn's route and
sender (`routd/agent_gate.go:34-37`), in an elevated `/root` turn the issuer
of the `/root` row, which `turnMCP.trigger` carries once BUGS `L666` is fixed
(§"An operator is an evaluator verdict"), and at an
invite's redemption the issuer the invite records.

Root is a grant, not an exemption (`specs/5/33-paths-roles.md`, Decision 1).
An operator's sub expands to `role:operator`, whose default row holds `*` on
`**` with the option, so `CheckGrant` admits every write it admitted under the
exemption. A token with an empty folder whose sub holds no root grant loses
the acl writes it had; no Go client writes `/v1/acl` (`grep -rn '/v1/acl'`
finds only dashd's GET at `dashd/grants_admin.go:331`).

The host CLI is the trust root. It holds the store's write credential, so a
check inside it guards nothing a direct write could not bypass, and
`specs/5/32-acl-unified.md` §"Bootstrap" makes the operator's first grant
there. `arizuko group grant` and `arizuko apply` write without `CheckGrant`.

The operator overload is one test, `store.OperatorShape`, on the raw row:
scope `**`, the action omitted, no effect other than allow, no params or
predicate. Both routd (`routd/acl_resource.go:234-239`) and the CLI
(`cmd/arizuko/main.go:617-620`) call it, and it renders the edge to
`role:operator`, which `CheckGrant` checks as an edge write. An explicit
`admin` or `*` at `**` is an ordinary row on every door.

A folder's `role:member` and `role:world` edges ride its creation batch, whose
own gate authorizes the creation, and a pairing edge is added only with the
account owner's consent (`specs/5/31-identity-pairing.md`). Neither passes
`CheckGrant`.

## Invitations and unpairing are grant writes

```
 invite_create, /invite, dashd invites (changed)
   └─ CheckGrant(issuer, add admin on the target) ─► onbod mints, issued_by = issuer
 GET /invite/<token> (changed)
   └─ CheckGrant(recorded issuer, add admin on the target) ─► acl row + BumpAt(v)
                                                    refused → the invite stays unspent
 unpair, REST and agent (changed)
   └─ CheckGrant(caller, remove edge child → parent) ─► edge delete + BumpAt(v)
```

An invite is a grant written later by someone else, so it passes the gate
twice: when it is issued, with the issuer's whole Caller, and when it is
redeemed, with the issuer it records, so a redemption after the issuer lost
the authority is refused. The row is `admin` on the target, or `admin` on
`<target>*/**` for a subgroup invite, whose redemption creates one child and
its admin. An issuer of `cli`, `onbod` or `routd` is a holder of the store's
or onbod's bearer, the trust root, and is not checked; `/invite` records its
sender in place of `routd`.

Current, the agent door's only gate, `ipc/ipc.go:2160-2162`, and the
redemption's write, `onbod/main.go:1217-1222`:

```go
			if err := authzStructural("invite_create", targetGlob); err != nil {
				return toolErr(err.Error())
			}

	if split && !strings.HasSuffix(target, "/") {
		if perr := db.AddACLRow(core.ACLRow{
			Principal: userSub, Action: "admin", Scope: target,
			Effect: "allow", GrantedBy: "invite",
		}); perr != nil {
```

Proposed, routd's `CreateInvite` closure (`routd/mcp.go:144-154`), which holds
the turn:

```go
		CreateInvite: func(targetGlob, issuedBySub string, maxUses int, expiresAt *time.Time) (ipc.InviteInfo, error) {
			// An invite hands out what its redemption writes; the turn must hold it.
			if _, err := auth.CheckGrant(s.db.store(), s.turnGranter(t),
				auth.Mutation{Row: store.InviteGrantRow(targetGlob)}); err != nil {
				return ipc.InviteInfo{}, err
			}
			// ... unchanged
		},
```

The redemption runs the same call with `auth.Caller{Principal:
issuerPrincipal(inv.IssuedBySub)}`, where `agent:<f>` is `folder:<f>`, and
proves its acl write with the version (BUGS `L664`). The same check in
`consumeInvite` covers the single-store path (`store/invites.go:438-449`).

`unpair` removes an edge whose parent may hold denies, and the child's turns
lose them (BUGS `L665`). It is an edge removal like any other: an account with
no deny unpairs as today, and an account with a deny unpairs only through a
caller that could grant each denied action, which `role:operator` can. Its
`contain` checks (`routd/membership_resource.go:83-90`, `:99-110`) stay as the
door's gate.

## An operator is an evaluator verdict

```
 proxyd → auth.UserScopes (changed) → X-User-Groups → auth.IsOperator (unchanged)
          lists ** only when Authorize(sub, admin, **) allows
 routd IsOperator (changed) = Authorize(sub, admin, **)
   → /root, /status, /invite, /gate (unchanged callers)
 elevated /root turn (unchanged): root for one turn
```

Current, `routd/sibling_db.go:265-273`:

```go
func (d *DB) IsOperator(sub string) bool {
	if sub == "" {
		return false
	}
	if slices.Contains(d.store().Ancestors(sub), "role:operator") {
		return true
	}
	return auth.Authorize(d.store(), auth.Caller{Principal: sub}, "admin", "**", nil)
}
```

Proposed:

```go
func (d *DB) IsOperator(sub string) bool {
	return sub != "" && auth.Authorize(d.store(), auth.Caller{Principal: sub}, "admin", "**", nil)
}
```

`UserScopes` stays a listing, and it lists `**` only when `Authorize(sub,
"admin", "**")` allows. proxyd computes it on every request
(`proxyd/main.go:982`), so every dashboard gate that reads `**` in the header
(`auth/dashauth.go:54-56`) reads an evaluator verdict, and the gap BUGS `L659`
records for the daemons that hold no store closes.

An elevated `/root` turn stays root for its one turn: every call allowed, no
hold, every chat readable (`routd/agent_gate.go:30-32`, `ipc/ipc.go:1089`).
The verdict is read once, at `/root`, where `cmdRoot` asks `IsOperator`
(`routd/steer.go:301`). Asking the issuer's rows at each call instead would
replace every `IsRoot` read in ipc and routd with an evaluator question, and
the listing views among them (`ListRoutes`, `ErroredChats`, `ListTasks`, the
groups snapshot) ask no target to put to it. No hold is needed: the issuer
holds `admin` on `**`, the authority that answers a hold. The caveat: a deny
an operator holds below `**` does not reach their root turn; an operator who
must be refused somewhere is taken out of `role:operator`. The root turn
serves only its issuer: today it also runs the rows another sender adds to
its batch, under that sender's name (BUGS `L666`).

## dashd pages ask the evaluator

```
 dashd request → auth.ReadPolicy(sub), once per request (changed)
   ├─ visible(folder) = Allows("interact", folder) (changed)
   └─ operator        = Allows("admin", "**") (changed)
 X-User-Groups as the visibility listing (removed)
 CallerGroups as Extra principals in adminOn (removed)
```

Current, `dashd/authz.go:55-66` and `:198-205`:

```go
func visible(allowed []string, operator bool, folder string) bool {
	if operator {
		return true
	}
	if folder == "" {
		return false
	}
	return auth.MatchGroups(allowed, folder)
}

func (d *dash) adminOn(r *http.Request, scope string) bool {
	if d.routdStore() == nil {
		return false
	}
	caller := auth.Caller{Principal: reviewerSub(r), Extra: auth.CallerGroups(r)}
	return auth.Authorize(d.routdStore(), caller, "admin", scope, nil)
}
```

Proposed:

```go
// visible reports whether the caller may see folder's pages: the evaluator's
// verdict on `interact`, the floor `admin` and `*` cover, so a deny hides a
// page. p is read once per request; a count over every folder asks it per row.
func visible(p auth.Policy, folder string) bool {
	return folder != "" && p.Allows("interact", folder, nil)
}

func (d *dash) adminOn(r *http.Request, scope string) bool {
	if d.routdStore() == nil {
		return false
	}
	return auth.Authorize(d.routdStore(), auth.Caller{Principal: reviewerSub(r)}, "admin", scope, nil)
}
```

A scope string is not a principal: `CallerGroups` folded `acme/**` into the
expanded set, where only a wildcard-principal row could match it. The caveat:
a principal that holds only `mcp:<tool>` on a folder stops seeing its pages.
proxyd's `/priv` still reads the listing (BUGS `L493`), which this spec does
not change.

## `/always` writes only a concrete rule

```
 held call → withMatcher (changed): keys declared?
   ├─ yes → Always line, ♾ button; /always → CheckGrant → the row
   └─ no  → "Always is not offered: <tool> declares no standing rule"
 holdShapes' tool-wide fallback (removed)
```

Current, `routd/pending_actions.go:561-571`:

```go
func pinnedSpec(tool string, scalars map[string]string) (string, error) {
	pinned := map[string]string{}
	for _, k := range holdShapes[tool].keys {
		v, ok := scalars[k]
		if !ok {
			return "", fmt.Errorf("argument %q: the call carries no scalar value to pin", k)
		}
		pinned[k] = v
	}
	return auth.ParamsSpec(pinned)
}
```

Proposed:

```go
// pinnedSpec is the params spec of an `always` on tool: its declared keys at
// the held call's values. A tool that declares no key has no standing rule a
// button can write; the operator writes one on the grants page.
func pinnedSpec(tool string, scalars map[string]string) (string, error) {
	keys := holdShapes[tool].keys
	if len(keys) == 0 {
		return "", fmt.Errorf("%s declares no standing rule; an operator writes one", tool)
	}
	pinned := map[string]string{}
	for _, k := range keys {
		// ... unchanged
	}
	return auth.ParamsSpec(pinned)
}
```

The refusal fills `MatcherError`, which every sink already reads: the chat
shows no ♾ button and `/always` is refused with the same text
(`routd/pending_actions.go:360-364`). The row `/always` writes is a `hold:`
row, so it passes `CheckGrant` as any other door's: the reviewer holds `admin`
on the folder, the check that answering the call already made, and its batch
proves the verdict and the row with one version. Today only `like` declares a
key (`routd/pending_actions.go:518-523`).

## What this deletes

- `auth.namespacedAllows`, the second fold (`auth/authorize.go:609-646`), and
  `CheckHold`'s own read; `CheckHold` and `ShellEnvKeys` call `Allows`.
- `auth.Delegate`, `grantableRows`, `denyRows`, `blockedByDeny`
  (`auth/delegate.go:34-127`); `scopeCovers` and `paramsCover` move to
  `auth/grant.go`.
- The root exemptions at `routd/acl_resource.go:67` and `:159`, and dashd's
  allow-only branch (`dashd/grants_admin.go:229-237`).
- The acl half of `SeedRoutdFloor`, with `memberVerbs`, `alwaysMountedTools`,
  `skipApprovalFloor` and four of its five fixed instants
  (`store/seed.go:29-58`, `:66-111`).
- onbod's `hold:mcp:*` row in writeWorld (`onbod/main.go:933-939`).
- `aclOperatorShape` (`routd/acl_resource.go:234-239`) and `operatorRow`
  (`cmd/arizuko/main.go:617-620`), for one `store.OperatorShape`.
- routd `IsOperator`'s membership shortcut (`routd/sibling_db.go:269-271`).
- dashd's listing as visibility and `adminOn`'s `Extra` (`dashd/authz.go:23-47`,
  `:55-66`, `:202`).
- The tool-wide `/always` (`routd/pending_actions.go:516-517`).
- From `6/49-approval-ask.md`: Decision 5's floor batches and the proposed
  `acl_floor` entity, never built.

## The live rows after the deploy

Measured 2026-11-04 with `arizuko get -i <instance> acl` built from
`bd6f74091`; the audit runs again on each instance before its deploy.

- Neither krons nor sloth holds a `hold:mcp:*` row, so neither has a world
  made with the stored blanket hold.
- Each holds the 23 `role:member hold:mcp:<tool> ** deny` rows, local copies
  of defaults, and neither holds the two `service:dashd` rows (BUGS `L473`).
  The audit also counts the copies of the root row and the 13 member verbs.
  A local copy matches the calls its default matches and gives the same
  answer, so the release changes a verdict only where a default has no local
  copy: on both, the two `service:dashd` rows.
- krons holds four skip rows and one hold row on one folder. They decide that
  folder's calls to their tools, as today.

The copies hide future changes to their defaults: a tool taken out of
`skipApproval` still skips where a copy stands. And BUGS `L273` closes only
where the copy is gone, since a copied skip row and an operator's hold row on
the same call are both local, and the deny wins. Removing a copy is not a
revoke: the default it copied answers again. Removing the copies is an
operator action on live data, done per instance after the audit with
`arizuko group ungrant`. A world with a stored `hold:mcp:*` row needs its
`role:world` edge and that row's removal first, or it holds its messaging
once the skip copies are gone.

## Acceptance

Tests in the shipping commit, each failing without the change, every tool
spelled as production passes it:

- `auth`: a folder in `role:world` and `role:member` with no local row:
  `reply` runs inline, `schedule_task` is held.
- `auth`: the same folder out of `role:member`: `reply` is held.
- `auth`: a world with a local `folder:w hold:mcp:reply w allow`: `reply` is
  held, `like` runs inline (BUGS `L273`).
- `auth`: a folder with a local `hold:mcp:*` and the 23 local skip copies
  minus `delete`: `delete` is held, `reply` runs inline.
- `auth`: a world with a local `/always` row on `like(reaction=👀)`: a 👀 like
  runs inline from the local row, a ❤️ like from the default.
- `auth`: an empty `acl`: `service:dashd` holds `chats:update` on any folder,
  and a member of `role:operator` holds `*` on `**`.
- `auth`: a failed membership read refuses `Authorize` and returns an error
  from `CheckHold`; a role's deny is never dropped; a scope a replace holds
  refuses `Authorize`.
- `auth`: `CheckGrant` refuses `X` on `a/b/*` from a granter holding `X` on
  `a/**` with the option and a deny on `a/*/x`; refuses `a/[x]` from a granter
  holding `a/???`; refuses `X` on `a/b/c/*` past a deny on `a/**/x`; refuses
  `X` from a turn whose sender is denied `X`; refuses `mcp:X` on `a` from a
  `role:operator` member with a local `mcp:X a allow` without the option;
  refuses removing that local allow from a granter without the option;
  refuses a skip row from a writer without `admin` on its scope; admits it
  from a world's creator; refuses removing a deny the writer could not grant;
  refuses the `role:operator` edge from a non-operator.
- `store`: a proved batch whose scope moved after the read is refused with
  `ErrPolicyMoved`; a proved revoke of an absent row answers its own "not
  found".
- `routd`: `add_acl` and `/v1/acl` by root pass `CheckGrant` for a row
  `role:operator` covers; `remove_acl` of a deny is checked.
- `routd`: `invite_create` from a world's agent holding `mcp:*` on `w/**` is
  refused (BUGS `L664`); `onbod`: a redemption whose issuer lost `admin` with
  the option is refused and the invite stays unspent.
- `routd`: `unpair` of an edge whose parent holds a deny the caller could not
  grant is refused, and of an edge whose parent holds none is admitted (BUGS
  `L665`).
- `dashd`: revoking a deny the admin could not grant as an allow is
  refused; adding a deny outside `hold:` is admitted; a caller holding
  `interact` on `a/**` and a deny on `interact` at `a/b` does not see `a/b`'s
  pages.
- `routd`: a held `schedule_task` shows no ♾ button and `/always` on it is
  refused; a held `like` keeps the button.
- `routd`: `IsOperator` is false for a `role:operator` member with a deny on
  `admin` at `**`; `UserScopes` for that sub lists no `**`.
- `routd`: `TestWorld_HoldsOnlyWhatAPersonShouldSee` builds the world with
  the `role:world` edge and stops listing the 23 names
  (`routd/hold_world_inversion_test.go:150-159`): every mounted tool is free
  exactly when `skipApproval` names it, a row that names no mounted tool
  fails, and three fixed anchors stay (`reply` and `get_work` free,
  `schedule_task` held).
- `store`: `SeedRoutdFloor` into an empty store writes no acl row and the
  base allowlist as today; `onbod`: writeWorld writes the `role:world` edge
  and no hold row.
- `make build && make lint && go test ./... -short` green.

## The text that states the rule

Each changes in the shipping commit, naming `auth/defaults.go` and listing no
rule:

- `GRANTS.md` and `auth/README.md` § Authorization is one row and one
  question: two layers, one evaluator, `CheckGrant`
- `specs/5/32-acl-unified.md` §"Evaluation — `auth.Authorize`
  (`auth/authorize.go:25`)" and §"Bootstrap", and Decision 3 of
  `specs/5/33-paths-roles.md`; BUGS `L540` closes
- `specs/5/31-identity-pairing.md`, the paragraph that calls both unpair
  directions de-escalation, and the same claim at
  `routd/membership_resource.go:7-8`
- `specs/6/26-personal-mode.md` §"The mechanics behind `always`"
- `template/web/pub/arizuko/reference/grants.html` §"Holding a call for a
  human" and its delegation section
- the `invite_create` description (`ipc/ipc.go:2146`), the comments at
  `onbod/main.go:917-925`, `store/seed.go:1-13`, `dashd/authz.go:15-22` and
  `routd/hold_world_inversion_test.go:1-11`
- `CHANGELOG.md` [Unreleased]; BUGS `L473`, `L273`, `L92`, `L664`, `L665` and
  `L667` close

## Open questions

1. A world's agent issues invites today with `mcp:*` on `w/**`, and neither
   the folder nor the world's creator holds `admin` with the grant option
   (`onbod/main.go:926-929`, `store/acl.go:187-191`). After the change only an
   operator, or a folder granted `admin` with the option, issues one
   (BUGS `L664`). Recommendation: accept it; an invite hands out `admin`, and
   delegation without the option is the rule `specs/5/33-paths-roles.md`
   decision 3 already states.
2. A person whose account holds a deny cannot unpair their own channel
   identity after the change; an operator does it (BUGS `L665`).
   Recommendation: accept it; the deny is the operator's, and so is the
   decision to drop it.
3. Remove the local copies of defaults on krons and sloth after the
   deploy, so the next change to a default reaches them and BUGS `L273`
   closes there? Recommendation: yes, per instance after its audit; the
   copies give today's answer until then.
