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
layers: the release's defaults, which are code in one file, and the local rows
in `acl`, which the operator and the agents write. For one call, the local rows
decide when any of them matches, and the defaults decide only when none does.
Every verdict reads one consistent state of the rules, and every write to them
passes one containment check in that same state: a writer never grants more
than it holds.

The owner's words, 2026-11-04 and 2026-11-05: "like a config a distro ships …
you do have a way to override", "cant rows be layered on top of another?",
"defaults in code ok but in a single place, well readable", "only allow always
for concrete rules … or you have to use some operator prompt", and on
delegation "you need to check the grant he is granting and reject that also".

## The problem — two evaluators, defaults that never arrive, write doors with different rules

Read at `a4e1cc97b`.

- **Two evaluators over one row.** `Authorize` folds deny-wins over the action
  lattice (`auth/authorize.go:88-122`); `CheckHold` and `ShellEnvKeys` fold the
  same rows again with exact matching (`auth/authorize.go:619-646`). Three
  gates decide from a listing: `auth.IsOperator` reads `**` in `X-User-Groups`
  (`auth/dashauth.go:54-56`, BUGS `L659`), routd's `IsOperator` answers from
  `role:operator` membership before any deny (`routd/sibling_db.go:269-271`),
  and dashd shows pages by the listing (`dashd/authz.go:55-66`) and folds its
  scope strings in as principals (`dashd/authz.go:202`).
- **The defaults are rows written once.** `SeedRoutdFloor` writes the 39 floor
  rows only into an empty `acl` (`store/seed.go:75-113`), so an older instance
  never receives a newer row (BUGS `L473`).
- **A default cannot yield.** The skip rows are `deny` on `role:member` at
  `**`, and a deny wins over every hold row (`auth/authorize.go:639-645`). An
  operator row that holds `reply` in one folder never holds it (BUGS `L273`).
- **Each write door has its own rule.** The agent socket and REST call
  `auth.Delegate` for an add by a non-root writer only
  (`routd/acl_resource.go:67`, `:159`); a remove passes unchecked. dashd checks
  an allow and lets any deny through (`dashd/grants_admin.go:229-237`), and a
  deny on a `hold:` action is a skip. `/always` checks that the reviewer holds
  `admin` (`routd/pending_actions.go:347`). `invite_create` checks only that the
  agent may call the tool, and the redemption writes `admin` (BUGS `L664`).
  `unpair` removes an edge whose parent may hold a deny (BUGS `L665`). dashd's
  group delete purges the folder's `acl` rows and edges after only `admin` on
  the folder (`dashd/groups_admin.go:452`, `store/folder_scope.go:51-65`,
  BUGS `L669`).
- **`Delegate` covers more than the granter holds.** A held `a/???` covers the
  glob `a/[x]`; a deny on `a/*/x` does not block a grant on `a/b/*`, which meets
  it at `a/b/x` (BUGS `L667`).
- **A verdict reads the rules in pieces.** `Authorize` reads rows, membership
  levels and the route's ancestors with separate queries
  (`auth/authorize.go:267-316`, `:129-135`), so writes landing between them
  give a verdict no single state gives. A door's gate, `Delegate` and the write
  are two reads and a write, each at its own instant
  (`routd/acl_resource.go:154-166`).
- **Two operator overloads that disagree.** routd writes the `role:operator`
  edge for scope `**` with the action omitted or `*`
  (`routd/acl_resource.go:234-239`); the CLI writes it for `admin` at `**`
  (`cmd/arizuko/main.go:617-620`).
- **A failed read hides a deny.** `Ancestors` returns nothing when a membership
  read fails (`store/membership.go:376-378`); `CheckHold` holds nothing when the
  `acl` read fails (`auth/authorize.go:593-605`, BUGS `L92`).
- **`/always` widens to a whole tool.** A tool with no declared keys gets a
  tool-wide skip row (`routd/pending_actions.go:516-517`, `:561-571`).

## Where it fits

```
 auth/defaults.go (new)                      acl rows (unchanged): the operator,
 the release's rules, one file               /always, onbod, folder creation
        │ default layer (new)                       │ local layer (unchanged)
        ▼                                           ▼
 ┌─────────────────────────────────────────────────────────────────────────┐
 │ auth.ReadPolicy (new): one objstore.ReadConsistent over routd's scope   │
 │ Policy.Allows (new):                                                    │
 │   any matching local row → the local rows decide, a deny wins           │
 │   else                   → the default rows decide, a deny wins         │
 └───┬────────────┬─────────────┬─────────────┬──────────────┬─────────────┘
     │ Authorize  │ CheckHold   │ ShellEnvKeys│ UserScopes   │ CheckGrant (new):
     │ (changed)  │ (changed)   │ (changed)   │ (changed)    │ the door's question
     ▼            ▼             ▼             ▼              ▼ and containment, one read
  every gate,  holdGate      spawn env     proxyd's       acl rows, edges, unpair,
  IsOperator,                              X-User-Groups  dashd grants, /always,
  dashd pages                                             invites, worlds (changed)
  (changed)                                                      │ write + BumpAt(v) (new)
                                                                 ▼
                                                           GuardedBatch (unchanged)
 auth.namespacedAllows, auth.Delegate, SeedRoutdFloor's acl rows,
 tool-wide /always, dashd's listing as visibility, dashd group delete (removed)
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
  job: one consistent policy read, one fold; Authorize, CheckHold, ShellEnvKeys,
    EffectiveActions and UserScopes are views of it
  names:
    - 'Policy struct{ caller Caller; route []string; local, defaults []core.ACLRow }'
    - 'ReadPolicy(s *store.Store, caller Caller) (Policy, objstore.Version, error)'
    - '(Policy) Allows(action, scope string, params map[string]string) bool'
    - 'CheckHold(s, caller, tool, scope, params) (bool, error)'
  never: folds a row a second way, answers from a listing, or reads outside ReadPolicy
- path: auth/grant.go (new, replaces auth/delegate.go)
  job: the one gate every policy write passes, the door's acl question included
  names:
    - 'Ask struct{ Action, Scope string }'
    - 'Mutation struct{ Remove bool; Row core.ACLRow; Parent string }'
    - 'CheckGrant(s *store.Store, granter Caller, door Ask, m ...Mutation) (objstore.Version, error)'
    - 'scopeCovers(held, want string) bool'
    - 'mayMeet(a, b string) bool'
  never: writes, or exempts a writer by who it is
- path: objstore/consistent.go, objstore/marker.go, objstore/reach.go (changed)
  job: the consistent read returns its version; a Bump and an edge write can
    start from it
  names:
    - 'type Version string'
    - 'ReadConsistent(ctx, s, sc, read) (Version, error)'
    - 'BumpAt(sc Scope, v Version) Write'
    - 'AddEdge(ctx, s, sc, at Version, ...) (bool, error)'
  never: adds a method to Store or a field to Write
- path: store/writeset.go, store/acl.go, store/membership.go (changed)
  job: a proved set renders routd's Bump as BumpAt; a refusal says whether the
    rules moved; the writers outside resreg take the version they prove; one
    renderer per granted row; Ancestors reports a failed read; the operator
    overload is one test
  names:
    - '(*WriteSet).Prove(v objstore.Version)'
    - 'AddACLRow(row, v) error'
    - 'SeedMemberRole(ctx, s, folder, at objstore.Version, also ...) (bool, error)'
    - 'InviteGrantRow(targetGlob string) core.ACLRow'
    - 'WorldGrantRows(owner, folder string) []core.ACLRow'
    - 'OperatorShape(raw core.ACLRow) bool'
    - 'RoleWorld = "role:world"'
    - 'Ancestors(child string) ([]string, error)'
  never: imports auth; the empty version proves nothing
- path: store/seed.go (changed)
  job: seeds the base egress allowlist into an empty network_rules
  never: writes an acl row
- path: onbod/main.go, store/invites.go, store/invite_redemptions.go (changed)
  job:
    writeWorld writes WorldGrantRows and the role:world edge; a redemption and
    a world created from it pass CheckGrant with the invite's issuer as granter
    and prove the routd write that follows
  names:
    - 'Redemption.IssuedBy string'
  never: writes an acl row from store.consumeInvite, or proves a write on onbod's handle
- path:
    routd/acl_resource.go, routd/membership_resource.go, routd/pending_actions.go,
    routd/mcp.go, routd/steer.go, routd/sibling_db.go, dashd/grants_admin.go,
    dashd/invites.go (changed)
  job: every add, remove, edge removal and invite issue calls CheckGrant with
    its door's question and proves its write with the version; /always only for
    a tool with keys; holdGate blocks on a CheckHold error; an invite records the
    identity its check read; registerGroup's rollback deletes only its own row;
    IsOperator asks Authorize
  never: skips CheckGrant for root, asks the door's question in a second read,
    or decides operator from membership presence
- path: dashd/authz.go, dashd/main.go, dashd/groups_admin.go (changed)
  job: a page is visible when the caller's policy allows interact on its folder;
    group delete answers with the host CLI command
  never: reads X-User-Groups, makes a scope string a principal, skips a
    per-folder test for an operator, or purges a folder
```

`objstore`'s two methods do not change, and neither does `Write`: `BumpAt` is
`Bump` with a `Cond`, the compare-and-swap the versioned replace already writes
on the marker (`objstore/replace.go:189`). `AddEdge` is a function over the two
methods. The `acl` row, its six-field key (`store/acl.go:37`) and
`acl_membership` do not change.

## A rule, and the two layers it comes from

A rule is the row `specs/5/32-acl-unified.md` owns, unchanged: `core.ACLRow`,
`(principal, action, scope, params, predicate, effect, grant_option)`. The
layer is not a field. Every row in `acl` is local; every row in
`auth/defaults.go` is a default. A writer cannot make a default, so there is no
attribute to forge, and `granted_by` stays an audit field the evaluator never
reads.

Defaults reach every instance with the binary. Nothing is written at a start,
so an instance born before a default existed reads it at the next deploy, and a
local row survives every restart. Revoking the local row that overrides a
default brings the current default back. A package cannot declare `acl` rows
(`EXTENDING.md` § Package definition), so the release is the only source of
defaults.

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

Proposed, replacing the two folds (`auth/authorize.go:88-122`, `:619-646`):

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

func verdict(rows []core.ACLRow) bool {
	for _, r := range rows {
		if r.Effect == store.EffectDeny {
			return false
		}
	}
	return len(rows) > 0
}
```

Proposed, in `actionCovers` (`auth/authorize.go:391`) after its equality test:

```go
	// A `hold:` or `shell:env:` action is matched exactly or by its own `*`: an
	// operator's (`*`, `**`) row would otherwise hold every call it makes and
	// put every folder secret in its turns (spec 5/19 Hazard 2).
	for _, ns := range []string{HoldPrefix, ShellEnvPrefix} {
		if strings.HasPrefix(requested, ns) {
			return rowAction == ns+"*"
		}
	}
```

`matching` is the body of today's loop: the scope, predicate and params tests,
`actionCovers` for a deny and for a route-axis allow, and `withinSubtree` with
`identityCovers` for an identity-axis allow (`auth/authorize.go:105-114`). A
deny is never confined. `Authorize` is `ReadPolicy` then `Allows`.
`ShellEnvKeys` is `Allows` per key on `ShellEnvPrefix`. No `Authorize` call
asks a `hold:` or `shell:env:` action, so the isolation changes no `Authorize`
verdict. `EffectiveActions` and `UserScopes` list the allows of both layers.

The layer split changes no verdict outside `hold:`: the defaults hold no deny
in any other namespace, so the defaults a matching local row hides could only
add an allow to a verdict the local rows already gave. It does change what a
principal may delegate, so `CheckGrant` reads the deciding layer too.

`ReadPolicy` is the one read behind every verdict. It runs `expandPrincipals`
(both axes, `auth/authorize.go:289`, `:129`) and `policyRows`
(`auth/authorize.go:267`) inside one `objstore.ReadConsistent` of routd's
scope, which `acl` and `acl_membership` both move (`store/scopes.go:60-82`),
adds the default rows of the expanded principals, and returns the version it
read at. `ReadConsistent` exists and the export reads through it
(`objstore/consistent.go:24-42`, `resreg/archive.go:72`). It costs two point
reads of the marker per verdict, and a retry when a routd-owned write lands
inside the read. The caveat: while `arizuko apply` replaces routd's scope,
every verdict is refused, because the rows are part-replaced.

`CheckHold` is `ReadPolicy` of `folder:<f>` then `Allows(HoldPrefix+tool, f,
args)`, asked of the folder alone, as today (`routd/pending_actions.go:410`).
An approval needs `admin` on the folder (`routd/pending_actions.go:347`) and
the tool's own gate runs after it, so an approval never defeats a deny.

A read failure refuses. `ReadPolicy` returns the error of the `acl` read and of
each membership read, `Authorize` denies, and `CheckHold` returns the error,
which `holdGate` renders as a blocked call the way it renders a hold it could
not record (`routd/pending_actions.go:418-425`, BUGS `J3`); BUGS `L92` closes in
that direction.

## The defaults are one file, read top to bottom

```
 auth/defaults.go (new) ── defaultRows ──► ReadPolicy (the only reader)
 store/seed.go: memberVerbs, alwaysMountedTools, skipApprovalFloor and the
   acl half of SeedRoutdFloor (removed)
 onbod writeWorld: folder:w → role:world edge (new), folder:w hold:mcp:* row (removed)
```

Proposed, `auth/defaults.go`, the whole file, replacing `store/seed.go:32-58`
and the acl half of `:75-113`:

```go
package auth

// defaults.go is the release's access policy, the ONLY place it is written.
// The evaluator reads these rows under the local rows in `acl`: for one call,
// a local row that matches decides, and these decide only when none does. An
// operator overrides a default with a local row and gets it back by revoking
// that row. The docs name this file and never copy it.

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

The 40 rows are the 39 the seed writes plus the world's blanket hold, spelled
as production passes them: the hold gate passes `reply`, never `mcp:reply`
(`auth/checkhold_test.go:15-31` is corrected). `memberVerbs` and
`skipApproval` share 13 names and neither derives from the other.

The world's blanket hold is a default on `role:world` because a stored
`hold:mcp:*` row would make the local layer decide every hold of the world, and
the default skip rows would never be read. Its scope is `**` where writeWorld
wrote `w/**`; the hold is asked about the turn's own folder
(`routd/sibling_db.go:258-260`), so both match the same calls.

## CheckGrant — one gate for every write, in one read

```
 add_acl, remove_acl, agent unpair   door question: mcp:<tool> on the target ─┐
 dashd grants add, revoke            door question: admin on the scope ───────┤
 /always                             door question: admin on the folder ──────┤
 POST, DELETE /v1/acl; REST unpair   bearer scope, ownsFolder, sub = parent   │
   (unchanged, no acl read) ─────────────────────── empty question ───────────┤
 redemption, world from a redemption the invite's recorded issuer ────────────┤
                                                                              ▼
              auth.CheckGrant (new): one ReadPolicy of the granter and of every
              principal the write touches → version v; the door question, then
              what each mutation moves, and does the granter hold it?
                ├─ no  → 403, nothing written
                └─ yes → v
                                         │
                                         ▼
              GuardedBatch: the write, its audit row, BumpAt(v) (new);
              it commits only while routd's scope is still at v
 auth.Delegate (removed)   the door's separate acl read (removed)   the root exemptions (removed)
 host CLI: arizuko group grant, group rm, apply (unchanged, the trust root)
```

Proposed, `auth/grant.go`, replacing `auth/delegate.go:34-127` and its calls
(`routd/acl_resource.go:67`, `:159`, `dashd/grants_admin.go:229-237`), and
asking each door's acl question in the same read
(`routd/acl_resource.go:154`, `routd/membership_resource.go:85`,
`dashd/authz.go:208`, `routd/pending_actions.go:347`):

```go
// Ask is the door's own acl question: may the granter use this door on this
// target. Zero when the door's gate reads no acl row (a bearer scope, the
// account that owns a pairing, an invite's recorded issuer).
type Ask struct{ Action, Scope string }

// Mutation is one write to the rules: Row added or removed, or, when Parent is
// set, the membership edge Row.Principal → Parent added or removed.
type Mutation struct {
	Remove bool
	Row    core.ACLRow
	Parent string
}

// CheckGrant answers the door's question and refuses a write that hands out
// authority the granter does not hold, from one ReadPolicy. It returns the
// version of that read; the write's batch proves it.
func CheckGrant(s *store.Store, granter Caller, door Ask, m ...Mutation) (objstore.Version, error)
```

Each door, root included, calls it with its own question and the mutation,
answers a refusal with 403, and passes `v` to its write as `x.Writes.Prove(v)`.

What one write moves, and when it is contained:

| write                                                            | the authority it moves                                                        | contained when                                                                           |
| ---------------------------------------------------------------- | ----------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------- |
| add an allow, or remove a deny, outside `hold:` and `shell:env:` | the row's action on its scope, with its params                                | the granter's deciding layer holds it with the option, and no granter deny may meet it   |
| remove a local allow outside `hold:`                             | what the defaults under it may reveal                                         | as adding that allow with the option                                                     |
| add a deny outside `hold:`                                       | none: it narrows                                                              | always; the door decided who may write there                                             |
| add or remove any `hold:` row                                    | an approval standing for the calls it matches                                 | the granter holds `admin` on the row's scope                                             |
| add a `shell:env:` allow, or remove its deny                     | a folder secret in an agent's shell                                           | a granter allow with the option on the whole scope, action `*`, `shell:env:*` or the key |
| add the edge child → P                                           | every allow and `hold:` row P and its ancestors hold, both layers             | each row by the rules above                                                              |
| remove the edge child → P, a pairing edge included               | every deny, `hold:` row and local allow P and its ancestors hold, both layers | each row by the rules above                                                              |
| issue or redeem an invite; create a world from one               | `InviteGrantRow`; `WorldGrantRows`                                            | as added allows (§"Invitations, worlds and unpairing")                                   |

### The containment tests

Each test is conservative: an inclusion it cannot prove is a refusal.

- **Covers** is `coveredByGrantable` (`auth/delegate.go:133`) with the two
  namespaces isolated in `actionCovers`, `paramsCover` (`auth/delegate.go:178`)
  and a stricter `scopeCovers` (`auth/delegate.go:160-173`). A granter allow
  with a predicate covers nothing. The last scope test refuses every glob
  character `path.Match` reads, so a held `a/???` does not cover `a/[x]`
  (BUGS `L667`):

  ```go
  	// held has no recursive tail, so it covers only a want with no glob
  	// character: `?`, `[` and `\` widen a want as `*` does.
  	if strings.ContainsAny(want, `*?[\`) {
  		return false
  	}
  	return matchPattern(held, want)
  ```

- **May meet** replaces "one scope covers the other" (`auth/delegate.go:122`):

  ```go
  // mayMeet reports whether two scope globs can match one path. It answers
  // false only on proof: before the first `**` of either glob, segments align
  // one to one, so two segments without a glob character that differ there
  // keep the globs apart; and two globs without `**` match paths of their own
  // length only. A `**` makes every later alignment possible.
  func mayMeet(a, b string) bool
  ```

  `a/*/x` and `a/b/*` meet at `a/b/x`, so that grant is refused. A deny's
  params and predicate are not read: a deny that might apply counts.

- **The axes.** The covering allow comes from the route axis, or from the
  caller's own principal on a single-axis caller: a sender's allows never count
  toward what a turn may grant, since execution confines them to the route's
  subtree. The denies come from every principal of the caller, both axes
  (`auth/authorize.go:57-58`). A folder that holds `X` with the option cannot
  grant `X` in a turn whose sender is denied `X`.
- **The deciding layer.** When some local row of any of the granter's
  principals, either axis, may meet the wanted authority, the write is
  contained only when a local allow with the option covers it and no local deny
  may meet it; otherwise the same test runs on the default rows. A folder in
  `role:operator` with a local `mcp:X a allow` without the option cannot grant
  `mcp:X` on `a` through the default `*` on `**`.
- **A removal is a grant.** Taking a local allow away never widens a call's
  verdict outside `hold:`, but it can change which layer decides for some
  principal, on either axis or through a wildcard principal, and so reveal a
  default's grant option. CheckGrant does not search for who is affected:
  every removal of a local allow, and every local allow an edge removal takes
  from the child, is checked as adding that allow with the option. The caveat:
  a writer that passes the door but holds the row without the option cannot
  revoke it; an operator holds every allow with the option and revokes
  anything.
- **A wildcard principal** on an added allow is refused (`auth/delegate.go:54`),
  since the rows it reaches are not known at write time; a removed one passes
  the removal test like any other row.

### The proof

CheckGrant reads every row it decides on, the door's question included, inside
one `ReadPolicy`, so the rules it judges are one state, at version `v`. The
write's batch carries routd's Bump at that version, so it commits only while no
write to routd's scope has landed since: a revoke of the door's own grant, a new
deny, a grant-option change and a new edge all move it (`store/scopes.go:60-82`).
The Bump is a write the batch already makes, so no item is named twice
(`objstore/batch.go:108-120`). Bearer scopes, `ownsFolder` and the pairing
parent test read no rule and stay where they are.

Proposed, beside `Bump` (`objstore/marker.go:88-93`):

```go
// BumpAt is Bump for a write that rests on a consistent read: it moves the
// marker only while the marker is still at v.
func BumpAt(sc Scope, v Version) Write {
	w := Bump(sc)
	w.Cond = Cond{Where: []Cmp{{Attr: markerVersion, Op: Eq, Value: string(v)}}}
	return w
}
```

On a refusal `Apply` reads the marker once: moved, the answer is
`objstore.ErrScopeMoved` (`objstore/replace.go:35`), a 409 the caller sends
again; unchanged, it is the batch's own guarded write refusing, a revoke's "not
found" or `/always`'s verdict. The caveat: every routd-owned write moves the
same version, routes and tasks among them, so a grant beside one is refused and
sent again; a grant is a write at the pace of a person.

A proved write is made on routd's handle, since dynstore puts the handle's
owner in front of every entity (`store/served.go:17-19`,
`objstore/dynstore/dynstore.go:129-131`); onbod and dashd hold one
(`onbod/main.go:89`, `dashd/grants_admin.go:220`). `CheckGrant` runs in the
caller, never in `store`, since `auth` imports `store`
(`auth/authorize.go:10`); the caller passes `v` as
`x.Writes.Prove(v)`, `AddACLRow(row, v)` or `SeedMemberRole(..., v, ...)`.
`objstore.AddEdge` given a version refuses with `ErrScopeMoved` when the marker
is not at it (`objstore/reach.go:82-107`).

### Who the granter is

The granter is the Caller the door evaluates: the JWT sub on REST and dashd,
the reviewer on `/always`, the turn's route and sender on the agent socket
(`routd/agent_gate.go:34-37`), the `/root` issuer in an elevated turn, and at an
invite's redemption the issuer the invite records. routd names the turn's
granter in one place, `turnGranter(t)`. The `/root` issuer is `turnMCP.trigger`
only once BUGS `L666` is fixed, so `L666` ships first or in this commit.

Root is a grant, not an exemption (`specs/5/33-paths-roles.md`, Decision 1): an
operator's sub expands to `role:operator`, whose default holds `*` on `**` with
the option. A root token whose sub holds no root grant loses its acl writes; no
Go client writes `/v1/acl` (`git grep -n '/v1/acl'` finds only dashd's GET at
`dashd/grants_admin.go:331`). The host CLI is the trust root: it holds the
store's write credential (`specs/5/32-acl-unified.md` §"Bootstrap"), so
`arizuko group grant`, `group rm` and `apply` write without `CheckGrant`.

The operator overload is one test, `store.OperatorShape`, on the raw row: scope
`**`, the action omitted, no effect other than allow, no params or predicate.
routd and the CLI both call it, and it renders the edge to `role:operator`,
which `CheckGrant` checks as an edge write. An explicit `admin` or `*` at `**`
is an ordinary row on every door.

A folder's `role:member` and `role:world` edges ride its creation batch, whose
own gate authorizes the creation, and a pairing edge is added only with the
account owner's consent (`specs/5/31-identity-pairing.md`). Neither passes
`CheckGrant`.

### A purge runs only from the host CLI

A folder purge deletes every `acl` row scoped under the folder and every
membership edge whose child is under it (`store/folder_scope.go:51-65`), in
several writes, `groups` first (`store/groups.go:328-331`). Removing
`U → folder:f → R` takes R's denies from U on every folder, which `admin` on
`f` does not prove the right to lift, and one check before the purge proves
nothing about its later writes. So `arizuko group rm`
(`cmd/arizuko/group_rm.go:119`) is the only purge. dashd's delete
(`dashd/groups_admin.go:452`) answers with that command, as its
unreachable-runed page already does (`dashd/groups_admin.go:438-441`), and
routd's registration rollback (`routd/mcp.go:241`) deletes only the groups row
it wrote (BUGS `L669`).

## Invitations, worlds and unpairing are grant writes

```
 invite_create, /invite, dashd invites (changed)
   └─ CheckGrant(issuer, InviteGrantRow) ─► onbod mints, issued_by_sub = the identity read
 GET /invite/<token> (changed)
   └─ CheckGrant(recorded issuer, InviteGrantRow) ─► consume (unchanged)
        ├─ a folder invite:   AddACLRow(InviteGrantRow, v) on routd's handle (changed)
        └─ a subgroup invite: redemption with issued_by_sub (changed)
 create_world from a redemption (changed)
   └─ CheckGrant(the redemption's issuer, WorldGrantRows) ─► spend
        ─► creation batch: WorldGrantRows + edges, AddEdge at v (changed)
 unpair, REST and agent (changed)
   └─ CheckGrant(caller, remove edge child → parent) ─► edge delete + BumpAt(v)
```

An invite is a grant written later by someone else, so it passes the gate
where it is issued and again before each routd write that acts on it, and each
check reads the row its write writes, from one renderer. `InviteGrantRow` is
`admin` on the target, or on `<target>*/**` for a subgroup invite, and the
redemption writes it. `WorldGrantRows(owner, folder)` is the owner's `admin` on
`<folder>/**` (`store.AdminGrantWrite`, `store/acl.go:187-191`) and the
folder's `mcp:*` on `<folder>/**` (`onbod/main.go:926-929`), and writeWorld
writes exactly those. An issuer narrowed after the redemption to `admin` on the
new folder alone cannot create it, because the creation grants `<folder>/**`.

At issue the check only refuses: the invite confers nothing yet, so no version
is carried and the door's gate stays where it is (`ipc/ipc.go:2160`). The
invite records the identity the check read: `agent:<f>` from a turn
(`ipc/ipc.go:2172`), the `/root` issuer from an elevated turn, the sender from
`/invite`, the sub from dashd. The recheck reads that identity alone, with
`agent:<f>` as `folder:<f>`; an issuer of `cli`, `onbod` or `routd` holds a
trust-root bearer and is not checked. The caveat: an invite from an elevated
turn is not in that folder's `invite_list` (`ipc/ipc.go:2196-2200`).

A redemption runs `CheckGrant` on onbod's routd handle before the consume, so a
refusal leaves the invite unspent, then writes `AddACLRow(row, v)` (BUGS
`L664`). When that write is refused, onbod calls `RestoreInvite`
(`onbod/main.go:1223-1236`) and asks the person to open the link again; when
the restore also fails, the invite stays spent and the failure is logged. onbod
always consumes with `ConsumeInviteNoGrant`, since split is the only topology
(`onbod/main.go:70-76`), so `ConsumeInvite` and `consumeInvite`'s acl branch
(`store/invites.go:438-449`) go and the row has one writer.

A subgroup redemption records the invite's issuer in a new `issued_by_sub`
attribute. `create_world` (`onbod/main.go:750-770`) checks that issuer before
it spends the redemption, refuses a redemption with no recorded issuer, and
proves its creation batch through `SeedMemberRole`; a moved scope runs the
check and the batch again, at most `maxCASAttempts` times. The caveat: a revoke
between the spend and the batch leaves the redemption spent and no world.

`unpair` is an edge removal: an account with no deny and no local allow unpairs
as today, and one holding either unpairs only through a caller that could
grant each of them with the option, which `role:operator` can (BUGS `L665`). A
person invited to a world holds `admin` there without the option, so that
person's unpair goes through an operator. The `contain`
checks (`routd/membership_resource.go:83-110`) supply the door: the agent's
`mcp:unpair` on the target becomes CheckGrant's question, and the REST parent
test stays as it is.

## An operator is an evaluator verdict

```
 proxyd → auth.UserScopes (changed) → X-User-Groups → auth.IsOperator (unchanged)
          lists ** only when Authorize(sub, admin, **) allows
 routd IsOperator (changed) = Authorize(sub, admin, **)
   → /root, /status, /invite, /gate (unchanged callers)
 elevated /root turn (unchanged): root for one turn
```

Proposed, replacing `routd/sibling_db.go:265-273`:

```go
func (d *DB) IsOperator(sub string) bool {
	return sub != "" && auth.Authorize(d.store(), auth.Caller{Principal: sub}, "admin", "**", nil)
}
```

proxyd computes `UserScopes` on every request (`proxyd/main.go:982`), so every
dashboard gate that reads `**` in the header reads an evaluator verdict, and
BUGS `L659`'s gap for the daemons with no store closes.

An elevated `/root` turn stays root for its one turn (`routd/agent_gate.go:30-32`,
`ipc/ipc.go:1089`); the verdict is read once, where `cmdRoot` asks `IsOperator`
(`routd/steer.go:301`). Asking at each call would put an evaluator question
behind every `IsRoot` read, and the listing views among them ask no target. The
caveat: a deny an operator holds below `**` does not reach their root turn; an
operator who must be refused somewhere is taken out of `role:operator`. The
root turn serves only its issuer once BUGS `L666` is fixed.

## dashd pages ask the evaluator

```
 dashd request → auth.ReadPolicy(sub), once per request (changed)
   ├─ visible(folder) = Allows("interact", folder) (changed)
   └─ operator        = Allows("admin", "**") (changed)
 X-User-Groups as the visibility listing (removed)
 CallerGroups as Extra principals in adminOn (removed)
```

Proposed, replacing `dashd/authz.go:55-66`:

```go
// visible reports whether the caller may see folder's pages: the evaluator's
// verdict on `interact`, which `admin` and `*` cover, so a deny hides a page.
func visible(p auth.Policy, folder string) bool {
	return folder != "" && p.Allows("interact", folder, nil)
}
```

`adminOn` (`dashd/authz.go:198-205`) asks `Authorize` with the reviewer's sub
alone and no `Extra`: a scope string is not a principal, and `CallerGroups`
folded `acme/**` into the expanded set, where only a wildcard-principal row
could match it. `callerScope`
(`dashd/authz.go:23-47`) returns the policy; its `operator` opens what only an
operator sees and nothing else. A per-folder filter asks `visible` alone, so
the operator shortcuts beside the filters go (`dashd/main.go:1063`,
`dashd/authz.go:141-147`). The caveat: a principal that holds only
`mcp:<tool>` on a folder stops seeing its pages. proxyd's `/priv` still reads
the listing (BUGS `L493`).

## `/always` writes only a concrete rule

```
 held call → withMatcher (changed): keys declared?
   ├─ yes → Always line, ♾ button; /always → CheckGrant → the row
   └─ no  → "Always is not offered: <tool> declares no standing rule"
 holdShapes' tool-wide fallback (removed)
```

Proposed, in `pinnedSpec` (`routd/pending_actions.go:561-571`), before the loop:

```go
	keys := holdShapes[tool].keys
	if len(keys) == 0 {
		return "", fmt.Errorf("%s declares no standing rule; an operator writes one", tool)
	}
```

The refusal fills `MatcherError`, which every sink reads: the chat shows no ♾
button and `/always` is refused with the same text
(`routd/pending_actions.go:360-364`). The row is a `hold:` row, so CheckGrant
asks the reviewer's `admin` on the folder, as the door's question and as the
`hold:` rule, in one read, and the batch proves the verdict and the row with
one version. Only `like` declares a key (`routd/pending_actions.go:518-523`).

## What this deletes

`auth.namespacedAllows` (`auth/authorize.go:609-646`); `auth.Delegate` with
`grantableRows`, `denyRows` and `blockedByDeny` (`auth/delegate.go:34-127`);
the root exemptions and door reads at `routd/acl_resource.go:67` and
`:154-166`; dashd's allow-only branch (`dashd/grants_admin.go:229-237`); the
acl half of `SeedRoutdFloor` with its lists and four of its five fixed instants
(`store/seed.go:32-113`); writeWorld's `hold:mcp:*` row
(`onbod/main.go:933-939`); `aclOperatorShape` and `operatorRow`
(`routd/acl_resource.go:234-239`, `cmd/arizuko/main.go:617-620`); routd
`IsOperator`'s membership shortcut; dashd's listing as visibility, `adminOn`'s
`Extra` and the operator shortcuts (`dashd/authz.go:23-66`, `:141-147`,
`dashd/main.go:1063`); dashd's group purge and the purge in routd's
registration rollback; `store.ConsumeInvite`, `consumeInvite`'s acl branch and
onbod's `split` switch (`onbod/main.go:1181`, `:1199-1202`); the tool-wide
`/always`; and from `6/49-approval-ask.md`, Decision 5's floor batches and the
proposed `acl_floor` entity, never built.

## The live rows after the deploy

Measured 2026-11-04 with `arizuko get -i <instance> acl` built from
`bd6f74091`; the audit runs again on each instance before its deploy.

Neither krons nor sloth holds a `hold:mcp:*` row. Each holds the 23
`role:member hold:mcp:<tool> ** deny` rows as local copies of defaults, and
neither holds the two `service:dashd` rows (BUGS `L473`). A copy gives its
default's answer, so the release changes a verdict only at the two
`service:dashd` rows. krons's four skip rows and one hold row on one folder
decide that folder's calls, as today. The copies hide later changes to their
defaults, and BUGS `L273` closes only where the copy is gone; removing them is
an operator action, per instance after the audit, with `arizuko group ungrant`.

## Acceptance

Tests in the shipping commit, each failing without the change, every tool
spelled as production passes it.

- `auth`, layers: a folder in `role:world` and `role:member` with no local row
  runs `reply` inline and holds `schedule_task`; out of `role:member` it holds
  `reply`; with a local `hold:mcp:reply w allow` it holds `reply` and runs `like`
  inline (BUGS `L273`); with a local `hold:mcp:*` and the 23 skip copies minus
  `delete` it holds `delete` and runs `reply`; with a local `/always` on
  `like(reaction=👀)` a 👀 like runs from the local row and a ❤️ like from the
  default.
- `auth`: an empty `acl` gives `service:dashd` `chats:update` on any folder and
  a `role:operator` member `*` on `**`; a failed membership read refuses
  `Authorize` and makes `CheckHold` return an error; a scope a replace holds
  refuses `Authorize`.
- `auth`, containment: `CheckGrant` refuses `X` on `a/b/*` past a deny on
  `a/*/x`; `a/[x]` from `a/???`; `X` on `a/b/c/*` past a deny on `a/**/x`; `X`
  from a turn whose sender is denied `X`; `mcp:X` on `a` from a `role:operator`
  member with a local `mcp:X a allow` without the option; a skip row from a
  writer without `admin` on its scope; removing a deny the writer could not
  grant; the `role:operator` edge from a non-operator.
- `auth`, removals: with folder `f` in `role:operator` and sender S holding a
  local `mcp:X f allow` without the option, removing S's row is refused from a
  granter without `mcp:X` on `f` with the option; the same for a
  `user:*` row and for the edge U → R that carries R's local allow; each is
  admitted from an operator.
- `auth` and `routd`, the door in the read: a granter holding `mcp:add_acl` and
  not `X` with the option, then losing `mcp:add_acl` and gaining `X` with the
  option between the read and the batch, has its write refused with
  `objstore.ErrScopeMoved`; a proved revoke of an absent row answers its own
  "not found".
- `routd`: `add_acl` and `/v1/acl` by root pass `CheckGrant`; `remove_acl` of a
  deny is checked; `unpair` of an edge whose parent holds a deny the caller
  could not grant is refused, of one whose parent holds none is admitted
  (BUGS `L665`).
- `routd` and `onbod`, invites: `invite_create` from a world's agent holding
  `mcp:*` on `w/**` is refused (BUGS `L664`); a redemption whose issuer lost
  `admin` with the option is refused and the invite stays unspent; an elevated
  `/root` turn in a folder without `admin` with the option issues an invite that
  records the operator's sender and redeems, and the unelevated turn is refused.
- `onbod`, worlds: a subgroup redemption whose issuer is then narrowed to
  `admin` with the option on the new folder alone is refused at `create_world`
  and stays unspent; a redemption with no issuer is refused; writeWorld writes
  `WorldGrantRows`, the `role:world` edge and no hold row.
- `dashd`: revoking a deny the admin could not grant is refused; adding a deny
  outside `hold:` is admitted; a caller with `interact` on `a/**` and a deny on
  `interact` at `a/b` sees no `a/b` page, row or count, operator or not; group
  delete removes nothing and names `arizuko group rm`.
- `routd`: a held `schedule_task` shows no ♾ button and `/always` on it is
  refused, a held `like` keeps it; `IsOperator` is false and `UserScopes` lists
  no `**` for a `role:operator` member denied `admin` at `**`; a registration
  whose route insert fails leaves an orphan deny under the folder in place;
  `TestWorld_HoldsOnlyWhatAPersonShouldSee` builds the world with the
  `role:world` edge and stops listing the 23 names
  (`routd/hold_world_inversion_test.go:150-159`): every mounted tool is free
  exactly when `skipApproval` names it, and `reply`, `get_work` free and
  `schedule_task` held stay as anchors.
- `store`: `SeedRoutdFloor` into an empty store writes no acl row.
- `make build && make lint && go test ./... -short` green.

## The text that states the rule

Each changes in the shipping commit, naming `auth/defaults.go` and listing no
rule: `GRANTS.md` and `auth/README.md` § Authorization is one row and one
question; `specs/5/32-acl-unified.md` §"Evaluation — `auth.Authorize`
(`auth/authorize.go:25`)" and §"Bootstrap", and Decision 3 of
`specs/5/33-paths-roles.md` (BUGS `L540` closes); the paragraph of
`specs/5/31-identity-pairing.md` that calls both unpair directions
de-escalation, and the same claim at `routd/membership_resource.go:7-8`;
`specs/6/26-personal-mode.md` §"The mechanics behind `always`";
`template/web/pub/arizuko/reference/grants.html` §"Holding a call for a human"
and its delegation section; the `invite_create` description
(`ipc/ipc.go:2146`) and the comments at `onbod/main.go:917-925`,
`store/seed.go:1-13`, `dashd/authz.go:15-22` and
`routd/hold_world_inversion_test.go:1-11`; `CHANGELOG.md` [Unreleased]. BUGS
`L473`, `L273`, `L92`, `L664`, `L665`, `L667` and `L669` close.

## Open questions

1. A world's agent issues invites today with `mcp:*` on `w/**`, and neither the
   folder nor the world's creator holds `admin` with the grant option
   (`onbod/main.go:926-929`, `store/acl.go:187-191`). After the change only an
   operator, or a folder granted `admin` with the option, issues one (BUGS
   `L664`). Recommendation: accept it; an invite hands out `admin`, and
   delegation without the option is the rule `specs/5/33-paths-roles.md`
   decision 3 states.
2. A person whose account holds a deny, or an allow without the option, cannot
   unpair their own channel identity after the change; an operator does it
   (BUGS `L665`). Every person an invite admitted holds `admin` without the
   option (`onbod/main.go:1217-1222`, `store/acl.go:187-191`).
   Recommendation: accept it; the rows are the operator's, and
   so is the decision to take them from an identity. The answer that keeps
   self-unpair is to make the deciding layer of a delegation read the route
   axis alone, so a sender's rows never hide a default; it is a second rule
   beside `Allows`, and this spec does not take it.
3. Remove the local copies of defaults on krons and sloth after the deploy, so
   the next change to a default reaches them and BUGS `L273` closes there?
   Recommendation: yes, per instance after its audit.
4. This spec rests on BUGS `L666`'s fix (a): the `/root` issuer is the elevated
   turn's granter and the issuer its invites record, which holds only when the
   root turn carries the issuer's row alone. Sign off `L666` (a) and ship it
   first? Recommendation: yes.
5. Revoking a local allow needs the authority to grant it with the option,
   including the cases where the removal reveals nothing: a folder admin
   without the option cannot revoke a grant in its folder. Accept the extra
   refusals? Recommendation: yes; the alternative is a search over both axes and
   every wildcard principal for who the removal affects, and an operator makes
   any removal.
6. dashd's group delete stops deleting and names `arizuko group <instance> rm`
   (BUGS `L669`). Accept losing the dashboard button until a purge passes
   `CheckGrant` per batch? Recommendation: yes; the purge lifts denies on other
   folders, and the CLI is the trust root that may.
