---
status: shipped
depends:
  [
    5/32-acl-unified,
    3/5-tool-authorization,
    4/19-action-grants,
    5/A-primitives-framing,
  ]
supersedes-in-part: [3/5-tool-authorization, 4/19-action-grants]
---

# specs/5/33 — paths, roles, and grant-option (tiers removed)

> arizuko does not derive capability from location. A **path** is where you are;
> a **role** is what you may do; a grant carries a `WITH GRANT OPTION` level that
> governs re-delegation. Authority flows by delegation — a subset of what you
> hold — never by a depth number.

`5/32` owns the `acl` row and the `Authorize` evaluator. **This spec owns the
model decision**: what an identity carries, where default power comes from, and
how authority moves between principals. The two do not overlap.

## Why

Capability used to leak from coordinate: `tier = min(count("/"), 3)`, default
power computed from that depth, and the authz boundary a folder-prefix walk. You
could not give a deep path broad power or a shallow path narrow power without
fighting the tree, and delegation was gated by an arbitrary number. The role
machinery to replace it already existed from the v0.38.0 ACL cutover — `acl`
rows, `acl_membership`, `role:` principals, transitive expansion, deny-wins — so
the tier axis was net cost.

## Decisions

1. **Root is a GRANT, not a location.** The operator holds a root grant and
   invokes it with `/root`. There is no root folder to occupy and no
   tier-0-by-position. `auth.Identity` carries `Folder` + `IsRoot` and nothing
   else (`auth/identity.go:14`).

2. **The path carries zero authorization.** `world/org/team` is a routing
   target, JID prefix, container home, and web vhost. Depth means nothing to
   authz.

3. **Grants flow by delegation, bounded by subset-of-held.** A principal may
   grant onward only rows it holds, and only those it holds WITH GRANT OPTION
   (`auth/delegate.go:28` `Delegate`, wired at `routd/acl_resource.go:212`).
   Authority strictly decreases down every chain without a depth number. A new
   group gets what its creating group or a lineage ancestor delegates.

4. **Lineage is canonical; the filesystem path is a projection** of it. Groups
   know about each other and may message/send files across the tree, gated by
   grants rather than tree position.

5. **Only worlds + delegated subgroups.** Top-level is a world (where users
   onboard, `5/18`); everything under it exists by delegation. No third
   structural tier.

6. **Mounts and egress are grants.** Filesystem share, network egress, and web
   publishing are resolved from `acl` at dispatch, not from depth
   (`routd/dispatch.go:532-534` → typed `ShareReadOnly`/`Egress`/`WebPublish`
   booleans on the run request). **Still open:** skill modification as a grant,
   with global skills operator-only — no skill-write tool exists yet, so there
   is nothing to gate.

7. **One default role: `role:member`, a functional floor.** The four per-depth
   bundles (`role:tier0..3`) collapsed to a single seeded role carrying the
   messaging verbs and `disengage` (`store/seed.go`); every folder is
   bound to it at create (`routd/seed_grants.go:16` `assignDefaultRole`). Read
   tools and `set_work` are always-on, no grant. Everything else — `register_group`,
   `routes`, `network_*`, `schedule_*`, `observe_*`, `invite_*`, token mint, `acl`
   — is explicit delegation. `role:operator` (`routd/migrations/0022`) is the
   root of every chain, seeded WITH GRANT OPTION.

   The migration is a deliberate demotion: a folder that sat above the floor by
   depth drops to `role:member`, and the operator re-delegates what it needs.

8. **Containment is the grant's scope glob, not a code prefix-walk.** Holding
   `mcp:register_group` scoped `acme/**` lets you register under `acme` and
   nowhere else. `auth.Authorize(caller, action, ACTUAL-target, params)` matches
   magnitude and containment in one call; a delegated row's scope IS its
   containment, so no containment rows are persisted separately.

9. **The `group`/`folder` → `path` rename is a SEPARATE later pass** and has NOT
   happened. Code, DB, MCP tool names, and env still say `folder`/`group`; this
   spec says "path" conceptually. Coupling a 300-file rename to an auth cutover
   is how both break.

10. **No tier scalar survives anywhere, including display.** `ARIZUKO_TIER` is
    not emitted (`container/runner.go:818`, pinned by
    `container/container_test.go:222`); the agent's identity is world + path +
    grants, never a rank.

11. **Inspect is the read face of the `acl` resource**, scoped to the caller's
    own effective grants — not a bespoke tool. Tool visibility
    (`auth.EffectiveActions`, `auth/authorize.go:86`), the per-call gate, and the
    prompt summary all read one `acl`, so they agree by construction.

12. **A blocked egress attempt must name the egress proxy** as the blocker, and
    the environment advertises that the agent sits behind one. A bare 403 reads
    like the target's own auth gate — the exact misdiagnosis `ant/CLAUDE.md`
    §"Network egress" warns about.

13. **A turn authorizes on two axes, and the identity axis is confined.** See
    §"Two axes" below. It is a decision of this spec because it answers "what
    does an identity carry"; `5/32` owns only how the evaluator reads it.

## Two axes

A turn is not one principal. It has a **route** — the folder the router resolved
it to — and an **identity** — the sender the channel attested. Until 2026-08-30
only the route reached authorization: every tool call ran
`Authorize("folder:"+folder, …)`, and the sender was computed
(`routd.turnCallerSub`) for the budget gate and dropped. `L147` names that shape
"attests and discards".

| axis         | field                   | set by                         | answers                            |
| ------------ | ----------------------- | ------------------------------ | ---------------------------------- |
| **route**    | `auth.Caller.Route`     | routd, at resolution           | what reaching through here confers |
| **identity** | `auth.Caller.Principal` | the edge (the attested sender) | what this person holds             |

They **union**, because `acl` is a union of grant rows — that is the only thing
a row set can do.

### The hazard, and the bound

A union can only WIDEN. Unbounded, an agent turn acquires the grants of everyone
who talks to it: hold a grant on tenant `X`, speak in tenant `Y`'s chat, and
`Y`'s agent reaches `X`. A shared chat becomes a place to borrow authority — a
confused deputy with the folder as deputy.

So an allow row reached ONLY through the identity axis carries **two bounds**,
both required:

1. **Containment** — the target must be the route's folder or beneath it. Not
   the row's scope: the TURN's folder. A speaker may add authority inside the
   conversation they are having, never outside it. A scope carrying a glob is
   never inside any subtree (`**` names targets outside every folder, and routd
   authorizes package management against exactly `**`).
2. **No lattice** — the action matches exactly, or through its own namespace
   wildcard (`mcp:*` for `mcp:<tool>`). `*` and `admin` cover every action, so
   reading the lattice here would make every message an operator sends silently
   elevate the turn that answered it. Elevation by identity already has a
   mechanism — `/root`, gated on `IsOperator`, one-shot, explicit
   (`routd.cmdRoot`) — and this must not become an implicit second one. It is
   the same refusal `auth.namespacedAllows` makes for holds and shell env, for
   the same reason (`5/19` Hazard 2).

**Deny is never bounded, on either axis.** A person's deny row refuses the turn
they triggered wherever it reaches. That is the only NARROWING a union of grant
rows can express, and it is the per-person containment `L146` asked for.

**The invariant, and it is tested.** Outside the route's subtree the verdict does
not depend on the sender at all: for every target the route's folder could not
reach before, it still cannot, whoever spoke
(`auth/authorize_axes_test.go` `TestAxes_NoReachOutsideTheSubtreeWhoeverSpeaks`).

### Nothing changes for an unpaired sender

Measured on krons: **164** distinct senders shaped `telegram:user/1112184352` and
`whatsapp:271317395882184@lid`, **one** pairing token, and **zero** `acl`
principals that are not `folder:` or `role:`. 163 of 164 match no row, so their
identity axis contributes ∅ and the turn authorizes exactly as before
(`TestAxes_UnpairedSenderIsUnchanged`). The axis is live but inert until an
operator writes a row keyed on a sender; that write is how per-person authority
turns on, one folder at a time.

**No sender→account resolution is implied.** A sender string is matched as it
arrives. Mapping a correspondent to a paired account is the inbound rewrite
`L146` proposes, and that is deferred; this spec adds the axis, not the mapping.

### One principal per turn

`ipc.StoreFns.Authorize` takes NO principal. A turn's principals are a property
of the turn, so `routd.turnAuthorize` closes over both and no call site can
supply a third. The socket's own `folder:<folder>` principal survives as the
audit actor and the resreg identity — never as an authorization input. Stacking
two notions of "who" into one gate is how a confused deputy gets built, so the
type makes it impossible rather than the convention discouraging it.

### Where the axis deliberately does NOT reach

Three reads stay single-axis on `folder:<folder>`, and each for its own reason:

- **Container capabilities** (`share_mount`, `egress`, `web:publish`, resolved at
  `routd/dispatch.go`) describe the SANDBOX, not a call. They are decided once
  per spawn; letting whoever spoke change what the container is mounted with
  makes the sandbox a function of the conversation.
- **`auth.ShellEnvKeys`** hands a folder secret into the agent's shell. Folding a
  person in would put their credentials in a container they do not control.
- **`auth.CheckHold`** answers "must this wait for a human?". Whether a call is
  held is a property of the folder's policy, not of who asked.

All three already refuse the action lattice for the same family of reason
(`5/19` Hazard 2). Adding an axis to any of them is a separate decision.

### Residual, recorded

A person explicitly granted `mcp:<tool>` inside a folder's subtree lends that
tool to the folder's agent for the turn they trigger. That is the feature, not a
leak: it is scoped to their own grant, bounded by the conversation's folder,
per-turn, and attributable. `mcp:*` on a sender is the deliberate all-tools
delegation, and it is bounded the same way.

## What shipped

One evaluator, `auth.Authorize` (`auth/authorize.go:25`), over `acl` rows with
deny-wins and **no fallback** — a folder with no matching allow row is denied,
loud (`auth/authorize_test.go:211` `TestAuthorizeWith_NoTierFallback`). The
parallel `[]string` grant plane (`grants/`), the folder-prefix containment
(`auth/policy.go` `AuthorizeStructural`), the persisted containment backfill
(`auth/backfill.go`), and the `RunRequest.Grants []string` transport are all
deleted. `grant_option` is a column on `acl`
(`routd/migrations/0021-acl-grant-option.sql`, `store/migrations/0075`).

**The one cost worth recording.** An early attempt to move egress/web off depth
routed the predicate through the tier scalar and was reverted (`3a8b8291`):
`container.tierOf` was `count("/")+1` while `auth.Resolve().Tier` was
`min(count,3)` floored to 1, so the two diverged below the top level and every
2-segment sub-group got unconstrained egress. The rule that came out of it —
never re-express a path predicate through a derived scalar; key equivalence
tests on real paths — is why decision 6 resolves from `acl` on the real folder.

## Open

- **Self-escalation ceiling.** With tiers gone, is subset-⊆-held enough, or is a
  per-principal non-delegatable ceiling wanted? (It must not reintroduce depth.)
  Today a child cannot exceed its granter, so escalation requires the granter to
  already be operator-equivalent.
- **Decision 9's rename pass** — its own spec, not started.
