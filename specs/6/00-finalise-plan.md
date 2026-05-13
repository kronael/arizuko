---
status: plan
depends: []
---

# Bucket 6 finalisation plan

Plan to take all four bucket-6 specs to `status: spec` so the bucket
can be marked ready. This is meta-work: writing only, no implementation.

## 2026-05-13 update — new constraint added

[`5-uniform-mcp-rest.md`](5-uniform-mcp-rest.md) was added as a sibling
principle to `R-platform-api.md`: every operator action accessible via
both REST (OAuth-gated) AND MCP (tier-gated) over a single handler.
Drops a registry of `Resource{Endpoints, MCPTools, Policy, Handler}`
into `auth/`; declares a `<resource>:<verb>[:own_group]` scope
vocabulary; pins a per-resource access matrix (grants, routes,
secrets, scheduled_tasks, chats, group_folders, egress_allowlist,
user_groups, invites).

Impact on the other bucket-6 specs:

- **`R-platform-api.md`** — its "Resource model" + MCP-federation
  sections were silent on whether the MCP tool tree is derived from
  the same registry as `/v1/*`. Spec 5 makes that mandatory. No
  contradiction; spec 5 is the explicit principle.
- **`1-auth-standalone.md`** — the `auth/` library gains `Caller`,
  `Resource`, `Endpoint`, `MCPTool`, `ScopePred`, `RegisterResource`
  in Phase A of spec 5 (alongside `auth.Mint`). One more surface in
  the same library.
- **`2-proxyd-standalone.md`** — unchanged. proxyd is still the REST
  gateway; the registry lives backend-side.
- **`4-openapi-discoverable.md`** — generated OpenAPI walks the same
  registry that MCP `tools/list` walks. Drift is structurally
  impossible; spec 4's generation source is the resource registry.
- **`specs/3/5-tool-authorization.md`** — the tier × action matrix
  becomes the scope minter at agent socket bind.

## 1. Per-spec audit

### `R-genericization.md` (status: draft) — long pole

Reads as a structured sketch with the skeleton right (Why now /
Coupling table / Target shape / Phases / Open). Substantial content
already on the page, but several blocks are nominally-named and need
filling.

Gaps:

- "Audit" step (Implementation phases #1, L130-132): one-line
  intention, no actual audit. The promised "what would break" inventory
  per daemon does not exist anywhere in the file or repo. **This is
  the largest single hole.** Needs a concrete per-daemon list of
  arizuko-type imports.
- "Phase A — generic primitives" table (L52-60) names six pairs but
  doesn't say how aliasing works at the Go level (type aliases vs
  newtypes vs interface wrappers). Migration semantics absent.
- "Phase B" (L66-78): two options listed without recommendation
  strength; lean is hedged. Needs explicit migration recipe — how a
  daemon stops sharing `gated`'s migration runner.
- "Phase C — gated split" (L80-95): names three new daemons
  (`routerd`, `agent-runnerd`, `mcp-hostd`) without ownership tables,
  surface inventories, or a "what stays in gated as a router" answer.
  Specifically: how does `ipc/`'s tool surface partition between
  `routerd` and `mcp-hostd`?
- "What's reusable" table (L107-116) — ranked but no acceptance test
  per daemon ("standalone-ready means X passes").
- Phase B.2 (L146-147) is a sentence; either drop it as deferred or
  state the trigger condition properly.
- "Open" (L148-160) — four bullets, two of which (`tenant_id` naming,
  capability-vs-tier perf) need a decision in this spec, not a punt.
- Cross-link to `specs/9/b-orthogonal-components.md` is mentioned but
  not woven into the Phase C narrative (crackbox is already the
  pattern this spec wants).

Writing sources:

- `README.md` daemon table (L48-66) — exhaustive daemon list to audit.
- `ARCHITECTURE.md` package graph (L28-60), SQLite schema (L192-220),
  message flow (L88-106) — concrete coupling evidence.
- `core/types.go`, `core/jid.go`, `auth/identity.go`, `auth/acl.go`,
  `grants/` — the arizuko-specific imports the audit must enumerate.
- `chanlib/chanlib.go` — already cited as nearest-to-generic shape.
- `specs/9/b-orthogonal-components.md` — sibling discipline crackbox
  already follows; mine for the "what makes a component truly
  orthogonal" checklist.

Finalisable as-is? **No.** Needs ~1.5–2 daemon-days of writing.

### `R-platform-api.md` (status: spec) — finalisable

A complete spec already: constraints, ownership table per daemon,
resource model, full token-claim JSON, issuance sites, verification,
MCP federation, dashboard migration table, implementation phases,
code pointers. Reads as ready.

Small gaps (polish, not blockers):

- "Open" (L283-303) — six items; TTL & revocation echoes the bucket
  index open question and needs a one-line cross-link to where the
  answer lands once decided.
- Code pointers (L304-321) reference `core/grants.go` — `core/`
  doesn't contain `grants.go`; `grants/` is its own package. Minor
  inaccuracy.
- The "ownership" table (L77-85) writes `grants` under gated; the
  R-genericization Phase C invents `routerd` that owns `tenants/rules`.
  These two specs need a single sentence acknowledging that grants'
  home will move during Phase C and the platform-API ownership table
  is the _post-Phase-B_ snapshot.

Finalisable? **Yes — needs a polish pass, no new sections.**

### `1-auth-standalone.md` (status: spec) — finalisable

Comprehensive. Four surfaces (verify/mint/HTTP/MCP), today's file
layout, target API signatures, mounting pattern, role-placement
table, phased plan, code pointers, "Open" + "Blueprint takeaway"
sections.

Small gaps:

- L34-39 hand-wave OAuth pending-state "in-process"; L148-152 then
  fixes that with a pluggable `StateStore`. The hand-wave can be
  removed.
- L283-300 "Open" — wildcard scopes lean is taken (`*:*` and
  `tasks:*` namespace-only); fold into the body so it's settled.
- The MCP-tool table (L169-174) lists four tools, none of which have
  named scope strings beyond "none". `mint_token` says "downscope-only
  enforced" — clearer to specify scopes as a unified row with the
  others.
- No mention of how this library coexists with the _existing_
  `auth/acl.go`-shaped grant evaluator beyond "move it out". Should
  name the destination package (`arizuko/identity.go`? `grants/`?)
  consistently with `R-genericization` Phase A.

Finalisable? **Yes — needs a tightening pass.**

### `2-proxyd-standalone.md` (status: spec) — concretized 2026-05-13

Complete: today's state, target TOML schema, HTTP management API,
per-route auth modes table, login flow, MCP tools, change table,
phased plan, code pointers, open questions. The "Per-daemon route
declarations" section (added 2026-05-13) lands the concrete
`[[proxyd_route]]` schema, loader model (per-daemon TOML →
`PROXYD_ROUTES_JSON`), per-route acceptance tests, and the
migration plan against current `proxyd/main.go` + `compose/compose.go`
line ranges. Stale numbers updated; four open questions locked into
"Decisions" (`[auth].mode = library`, boot-via-TOML only,
in-memory rate limits, audience-on-route).

Status: **spec, ready for Phase-2 implementation.** Remaining open
items are post-v1 (slink-token generalisation, core-daemon TOML
home choice).

## 2. Writing order

The bucket index claims genericization precedes API. Confirmed, but
with a refinement.

**Order:**

1. **R-genericization fill (the long pole).** Until the generic-types
   audit and Phase A names are pinned, `R-platform-api`'s ownership
   tables and `1-auth-standalone`'s `Identity.Extra` story keep
   referring to ungrounded concepts (`tenant_id` named but not
   chosen).
2. **R-platform-api polish.** Once Phase A landings are clear, fix
   the small inconsistencies (grants-home note, code-pointer paths,
   open-Q routing).
3. **1-auth-standalone polish + 2-proxyd-standalone polish** can
   happen in parallel after step 1, since both depend on the same
   `auth/` library boundary `R-genericization` Phase A pins down.

`R-genericization` is the gate. The other three are mostly written;
they're waiting on terminology and the gated-split shape.

## 3. Per-spec writing plan

### R-genericization

Sections it needs to gain (outlines only):

- **§ Audit** (replacing the one-line phase #1): per-daemon table with
  columns `daemon | imports from core/ | imports from store/ | imports
from chanlib/ | arizuko-specific symbols`. Mine: `README.md`
  daemon table, `go.mod` graph (run `go list` mentally per daemon
  directory), `ARCHITECTURE.md` package graph.
- **§ Naming decision.** Pick `tenant_id` (or alternative) and lock
  it. Add to Phase A table as the chosen name. Source: weigh
  `EXTENDING.md` "Permission tiers" (folder = identity) against
  generic platform vocabulary.
- **§ Phase A mechanics.** How aliasing works at Go-type level: type
  alias for compat, then call-site migration. Cite `core/types.go`
  and the `auth.Identity.Extra` pattern from `1-auth-standalone`.
- **§ Phase C ownership table.** Three new daemons × `owns / serves
/v1/ / hosts MCP tools`. Mine `ipc/ipc.go` for the split between
  routing-control tools and agent-host tools.
- **§ Capability-vs-tier perf.** Resolve the open question with a
  rough cost model — sub-µs per scope check, dominated by JWT
  verification. Cite `auth.RequireSigned` / `auth/middleware.go`.
- **§ Acceptance tests.** What "standalone-ready" means per daemon —
  e.g. "proxyd-standalone: deploy with one TOML route, one backend,
  one OAuth provider; arizuko deps unset; OAuth login works,
  reverse-proxy works, MCP tools register." One row per daemon.
- **§ Cross-link to specs/9/b-orthogonal-components.md.** Adopt the
  same discipline (no internal package imports) as the acceptance
  criterion for "reusable".
- **§ ContainerRuntime — pluggable sandbox backends** (DONE
  2026-05-13). Interface + 5-assertion contract test + backend
  catalog + migration path for the `agent-runnerd` seam. Mined from
  openclaw-managed-agents (`src/runtime/container.ts`,
  `container-contract.ts`) and hermes-agent (`BaseEnvironment` ABC).
  ~150 lines.

### R-platform-api

Sections it needs (light):

- **Note on grants home.** One sentence after the ownership table
  acknowledging the table is post-Phase-B; Phase C moves grants
  authority with `routerd`.
- **Fix code-pointer paths.** `core/grants.go` → `grants/`. Verify
  every code pointer.
- **TTL/revocation outcome.** Lock the lean (1h default, no
  revocation list in v1, long-lived dashd-keys deferred to a future
  revocation spec). Move from Open into the body.

### 1-auth-standalone

Sections it needs (light):

- **Fold wildcard-scope lean into body** (currently in Open).
- **Name the destination of `acl.go`/`policy.go`/`identity.go`**
  consistently with `R-genericization`'s Phase A target. Spec
  currently says `arizuko/identity.go`; if `R-genericization` lands
  `arizuko/domain.go` as the canonical home, harmonise.
- **OAuth pending-state.** Drop the "in-process" hand-wave at L34-39;
  point straight to `StateStore`.

### 2-proxyd-standalone (DONE 2026-05-13)

All five items landed in the spec (`specs/6/2-proxyd-standalone.md`):

- Folder-ref count updated to 3 active + 1 comment with file:line
  cites.
- `[auth].mode = library` locked under "Decisions"; `remote` reserved
  as future hook.
- Hot-reload locked to API-only; TOML for boot config.
- Rate-limit backend locked to in-memory.
- New "Per-daemon route declarations" section concretizes the
  `[[proxyd_route]]` schema, loader model, migration plan, and
  per-route acceptance tests — making the spec implementable directly
  for Phase-2 of the 6-phase ship.

## 4. Open-question routing

The four open questions at `specs/6/index.md` L46-52 land as follows:

| Open question                                | Lands in               | Section there                                                                                                           |
| -------------------------------------------- | ---------------------- | ----------------------------------------------------------------------------------------------------------------------- |
| TTL / revocation policy for tokens           | `R-platform-api.md`    | New "TTL & revocation" subsection under "Token model" (replaces the Open bullet at L285-287)                            |
| Per-daemon DB split (own DB vs shared)       | `R-genericization.md`  | Already framed as Phase B.1 / B.2 (L65-78, L144-147); decision text needs to be promoted from Open into the body        |
| Dedicated `authd` daemon                     | `1-auth-standalone.md` | Already declined in "Why no daemon" (L20-42); promote one sentence into bucket index as the resolution; no new section  |
| `gated` split into `routerd`/`agent-runnerd` | `R-genericization.md`  | Already framed as Phase C (L80-103); decision is "yes, after Phase B"; needs the ownership table called out in §3 above |

After the writing pass, the bucket index's Open block shrinks to a
"settled in spec X" pointer list and the bucket can move to
`status: spec`.

## 5. Implementation dependency graph

Constraints in play:

- The user's stated implementation order: **slakd ships first** (per
  `specs/2/l-slakd.md`, status spec).
- Bucket 6 is the platform/genericization phase; bucket 7
  (products) consumes it.
- `R-platform-api`'s implementation phases (L249-281) are
  independently shippable but share the auth-library upgrade as
  their floor.

Writing-only dependency tree (this plan covers only this):

```
                R-genericization fill (long pole, audit + Phase C)
                     │
       ┌─────────────┼──────────────┐
       v             v              v
R-platform-api  1-auth-standalone  2-proxyd-standalone
   polish          polish              polish
       │             │                  │
       └─────────────┴──────────────────┘
                     │
                bucket-6 status: spec
```

Implementation dependency tree (downstream, informational):

```
slakd (bucket 2, ship first per user direction)
     │
     v
auth lib refactor (extract arizuko-specific code, add Mint/MintNarrower)
     │
     v
proxyd TOML config + delegate Mint to auth lib
     │
     v
gated /v1/* (largest surface)
     │
     ├──> timed /v1/tasks
     │
     ├──> onbod /v1/{invites,users}
     │
     v
MCP host upgrade (agent token mint + HTTP forwards)
     │
     v
dashd migration (consume /v1/*; add write paths)
     │
     v
gated split (Phase C — routerd / agent-runnerd / mcp-hostd)
     │
     v
bucket 7 (products) consumes the federated API
```

The implementation graph is on the critical path _after_ slakd ships
and is informational here — bucket 6's writing work is the gate, not
the implementation.

## 6. Writing-effort estimate

In daemon-days (one focused day of writing per unit):

| Spec                      | Effort                                                                             | Notes                                                              |
| ------------------------- | ---------------------------------------------------------------------------------- | ------------------------------------------------------------------ |
| `R-genericization.md`     | ~1.5–2 days (ContainerRuntime ✓ 2026-05-13; ~0.5 day remaining for other sections) | Audit + Phase C ownership + Naming + Acceptance + ContainerRuntime |
| `R-platform-api.md`       | ~0.5 day                                                                           | Polish + code-pointer fixes + TTL section                          |
| `1-auth-standalone.md`    | ~0.5 day                                                                           | Wildcard scopes + harmonise paths                                  |
| `2-proxyd-standalone.md`  | ~0.5 day                                                                           | Stale-fact updates + lock open questions                           |
| Bucket-index Open cleanup | ~0.25 day                                                                          | Convert Open list to "settled in X" pointers                       |

**Total: ~3–3.75 daemon-days of writing** to take all four entries to
`status: spec` and shrink the bucket-index Open block.

## 7. Deal-breakers

None blocking. Two minor inconsistencies surfaced:

1. **Grants home.** `R-platform-api.md` (L86-89) places grants under
   gated; `R-genericization.md` Phase C (L85-88) gives `routerd`
   `tenants/rules/events`. These are reconcilable as "post-Phase-B"
   vs "post-Phase-C" snapshots but the bucket should call it out
   explicitly.
2. **Stale numeric facts in `2-proxyd-standalone.md`** (the "4 folder
   refs" claim is now 3 active + 1 comment). Easy fix during polish.

Neither contradicts a shipped spec.
