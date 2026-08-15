---
status: draft
---

# Adoption — interop first, campaign second, agentic reimplementation as the engine

arizuko is not a harness. It is orchestration: it decides how agents run and for
whom — routing, tenancy, identity, permissions — the layer _above_ any harness.
So the goal is not "stop running your harness and run arizuko"; it is "run your
harness inside arizuko." Adoption is addition, not replacement.

## Problem

The crowded field (Claude Code, OpenClaw, NanoClaw, ElizaOS, Hermes, a new one
every week) is a field of _harnesses_ — each runs one agent for one user.
arizuko is a different layer: it orchestrates agents across tenants. Competing
with a harness head-to-head is a category error; the win is to orchestrate it,
not replace it.

Two things make that layer worth adopting (`specs/5/A`, `USELESS.md` §4):

1. **The folder coordinate.** One path is tenant, ACL, route, egress, web host,
   and file tree — how agents run and for whom, on one box, the same code from
   `solo/inbox` to `corp/eng/sre/oncall`.
2. **Agents reshape the system; you stay in the loop.** The persona, skills,
   routing, even child groups are files the LLM edits — the agent changes the
   system, not just its output — yet every change is a file you diff, a grant
   that gates it, and (if you `git init` the group) a `git revert` away.
   Self-modifying, human-owned.

Neither requires replacing the harness. That is the wedge.

## Correction — the environment IS the product, not the wrap (2026-08-15)

The framing above ("run your harness inside arizuko") is **emulation**, and
operator direction on 2026-08-15 inverts it. Emulation inherits the wrapped
thing's model, carries compatibility debt forever, and caps arizuko at whatever
it wraps.

The Lambda analogy is the correction. Lambda never emulated your server. It
published a contract — handler, event, context, and a role the SDK finds on its
own — and the value was what the environment gives you for free: isolation,
IAM, scaling. You give up generality and gain the platform. **You write to their
APIs; they do not reimplement yours.**

arizuko has two such environments already, and neither is stated as a contract:

- **The agent environment**, which is `event + tools + home`:
  the prompt (`<autocalls>`, the `<observed>` window, the output style picked per
  surface), the MCP tool surface (14 hand-authored tools in `ipc/ipc.go` plus 18
  catalog resources whose tools are derived), and the folder home (`PERSONA.md`,
  `facts/`, `.claude/skills`, `public_html`, env-profile keys, egress allowlist,
  turn timeout). The tools are the API; the home is the half no competitor has.
- **The component environment**: `:8080` always, your own DB under
  `store/<owner>/` that you migrate, `obs.Setup`, `/openapi.json`, resreg
  resources behind an injected auth gate, an `audit_log` row in your own tx, a
  compose fragment that needs no edit to proxyd or the generator.

Both exist today as PROSE in root `CLAUDE.md`, not as anything that fails a test
when violated. Compare `TestResourceName_NoStringLiteral`, which IS a contract
because breaking it breaks the build. Nothing answers "is this a well-formed
arizuko daemon" or "is this a well-formed arizuko agent".

So the adoption work is: **name the two environments, write each as a contract,
give each a conformance test a stranger's part must pass.** Wrapping a foreign
harness stays available as an on-ramp — it is no longer the thesis. `6/16`'s
four-layer model is the map; this is the promise each layer makes.

## Two tracks, ranked

**1. Interop first (primary).** Do not ask anyone to switch. Let arizuko wrap
what they already run:

- **Runtime pluralism.** runed spawns "just another binary." Today that's Claude
  Code. Make the runtime swappable so an OpenClaw / a raw model / another harness
  runs inside an arizuko folder-agent. arizuko becomes the multi-tenant, routing,
  and web layer over _their_ agent, not a replacement for it (`USELESS.md` §5.3
  names this gap: one runtime today).
- **MCP as the seam.** arizuko is already MCP-native (agent side) + REST (human
  side). Every capability another harness exposes over MCP is reachable; every
  arizuko resource is reachable by their agent. Interop is protocol work, not a
  rewrite.
- **Import, don't convert.** Read their config: a Claude Code `settings.json` +
  skills, an OpenClaw channel map, a character.json. Mount it into a folder
  as-is. Adoption cost → near zero: keep your harness, gain folders + tenancy +
  egress + web.

**2. Campaign second (supporting).** Persuasion rides on proof, not slogans.
The credibility assets already exist — the Agent Research Hub (comparative,
source-cited), `USELESS.md` (the self-critique that makes honest claims
believable), and the `web-native Linux` one-liner (`specs/5/A`). The ordering
is the point: campaigning before the interop asks people to take a leap, where
interop makes the pitch "add arizuko," not "replace your stack."

## The engine — an agentic reimplementation loop

The interop surface is large (N harnesses × their features) and moving (each
ships weekly). Hand-building it does not scale. Dogfood instead: arizuko's own
agents build arizuko's interop.

A scheduled loop (`timed` → a folder-agent), one system at a time:

1. **Read** the target system's source (the Hub already clones repos into
   `refs/`) and its released changes since last run.
2. **Extract** the orthogonal component + the config/protocol surface an arizuko
   folder would need to host or import it.
3. **Reimplement / shim** the smallest interop that lets an arizuko folder run or
   import that system, in an isolated worktree.
4. **Verify** end-to-end (spawn a folder on that runtime, drive a turn), gate on
   an adversarial check, open the change for operator review.
5. **Record** in the Hub (that system's page) + a compatibility matrix.

This reuses machinery that already exists: the Hub's monthly code-analysis
protocol, worktree-isolated subagents, the migrate/broadcast delivery path. The
loop is the same shape arizuko already runs on itself (`specs/5/22` self-learning);
here it points outward.

## What the wrap provides — services one level up

Wrapping a harness is worth it only if arizuko supplies what the harness lacks.
Each row is a gap a bare harness has (Hermes is the worked example) that the
folder coordinate closes for _every_ harness in the fleet:

| Service          | arizuko mechanism                                                      | harness gap it closes                              |
| ---------------- | ---------------------------------------------------------------------- | -------------------------------------------------- |
| Keys / secrets   | folder-scoped encryption (`8/E`) + injection at the tool edge (`5/13`) | keys plaintext in the runtime; never host-injected |
| Egress           | per-agent folder-scoped allowlist + DNS filter (`6/8`, opt-in)         | one global blocklist; no per-tenant egress         |
| Identity / authz | folder-containment + grant DSL, injected Gate (`5/17`)                 | admin-vs-user binary; one shared bearer            |
| Tenant isolation | ephemeral per-turn container per folder                                | one shared long-lived container; users share FS    |
| Cost / budget    | per-tenant spend gate (`store/cost_log.go`)                            | per-session usage only; no caps                    |
| Audit / obs      | `audit_log` + `obs` (journald + OTLP)                                  | plaintext logs; no structured audit                |
| Routing / web    | route table + `/pub` `/priv` + WebDAV                                  | home-channel delivery only; no served web          |

Two ways to wrap, per capability: **run-as-is** (the harness runs inside a folder
via runed's swappable runtime) or **reimplement-in-Go** (port a _mechanism_, not
the whole process, into the native runed harness — "rewrite in Go" scoped to one
mechanism). The seven services above are the moat; never trade them for a
harness's model/skill breadth.

## Demand classes, not just systems

The loop also answers a _demand class_ (knowledge graphs, RAG, "codebase maps") —
a category of want every vendor restates as a reason to buy heavy machinery. The
discipline: strip to the primitive need, try the folder primitives first (tree +
files + `jq`/`grep`/FTS5, no new always-on subsystem), and escalate to a mounted
capability (RO `/mnt` or an MCP tool) only on proof the primitives can't. Same
interop-at-the-boundary rule, pointed at demands instead of systems (marble is the
reference output: a 1,590-topic prerequisite graph answered by `jq` over three
bundled JSON files, no graph DB).

## Non-goals / honest risks

- **Not a shim graveyard.** Interop that dilutes the folder coordinate is a
  liability. Every adapter must keep tenancy + egress + ACL intact, or it is not
  worth carrying.
- **The moat must stay the reason.** If arizuko is only "compatible with X," X
  wins on maturity. Interop is the on-ramp; the folder coordinate is why they
  stay. Lead with the wedge, not the compatibility list.
- **Validation is still autobiographical** (`USELESS.md` §3). One external
  operator running one imported harness inside arizuko is the first real proof —
  optimize the loop for that, not for feature count.
- **The reimplementation loop is unproven and expensive.** Scope it to one
  target, measure whether the interop it produces is actually adopted, before
  scaling the fleet.

## Ties

`specs/5/A` (positioning) · `USELESS.md` (honest gaps this closes) · the Agent
Research Hub (source of truth on targets) · `specs/5/22` self-learning (the loop,
turned outward) · runtime pluralism in runed.
