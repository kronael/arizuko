---
status: draft
---

# Components & daemons — what ships alone, and against what

The consolidated "ship in parts" spec: which pieces stand alone, on which
of two axes, measured against the market. Absorbs the former package
catalogue, the extraction method, and the orthogonal-component pattern.

## Two standalone axes (do not conflate)

A part can be "standalone" in two different senses; keep them apart.

1. **Import-orthogonal component** — a sibling dir, zero arizuko-internal
   imports, consumed only via CLI / HTTP / `pkg/` (the contract below). The
   _toolbox_ test: raid the repo for one part. Passers today: `crackbox`,
   `obs`; roadmap: `mcpfw` (MCP firewall, `6/12`) and `resreg` as a
   standalone two-face lib.
2. **Config-driven droppable daemon** — keeps arizuko imports, but runs
   generic against any stack because its behaviour is TOML/env, not
   hardcoded. The _service_ test: drop it in front of your backends.
   Passers/near: `proxyd` (`5/7`), `authd` (`5/1`).

Import-purity and deploy-genericity are different questions: a daemon can
be standalone-useful while importing half of arizuko (it runs as its own
process, consumed over a protocol), and a 0-import package can be
worthless alone (`core`, `theme`). A part that passes **neither** is still
correct — it is arizuko-inherent, and extracting it is a category error.

### The three-question rubric (before calling a part a component)

Import-count is a filter, not the verdict — a 0-dep package can be broken.
For each candidate:

1. **Contract** — the one promise it makes to a caller who knows nothing
   about arizuko. Can't state it in one sentence → not a component yet.
2. **Does it hold?** — test it adversarially. `crackbox` was the lesson: 0
   internal deps **and** its default-deny egress failed open (closed
   2026-08-15, below) — ready-by-import-count, broken-by-contract.
3. **Stranger vs cruft** — the minimal surface a stranger imports/runs,
   minus every folder/JID/`routd.db` assumption baked in. That delta is
   the extraction work.

### The component contract — arizuko owns domain, the component owns mechanism

An import-orthogonal component is a sibling dir with its own build, its own
`README` (the public surface), and its own state, importing zero
arizuko-internal packages — consumed only through its CLI, HTTP API, or
`pkg/`. The line that keeps it orthogonal: **arizuko owns _domain_ — what
an id means, folder hierarchy, grants, when to spawn; the component owns
_mechanism_ — a flat per-id ruleset, matching a request against it, opening
sockets.** So a component takes strings (`id`, `host`, `tool`) and returns
strings — never a `Folder`/`Grant`, never arizuko's `.env` or DB.
The mechanical test is one grep for `github.com/…/arizuko/(store|core|auth|
router|grants|ipc|…)` under the component dir → empty. `ant/` is the mirror:
TS in arizuko's repo, own build + Dockerfile, consumed as an image over a
stdin/stdout protocol, zero Go imports.

## The four layers — where a boundary can exist at all (2026-08-15)

The two axes above say whether a part passes; this says where a seam can be.
arizuko is four layers, and **each boundary is a contract, not a directory**:

1. **Surfaces** — channel adapters in, the web stack out. They speak the
   message envelope.
2. **Core platform** — routd (routing, turn record, bus), `resreg`, the folder
   coordinate. The layer that DEFINES contracts.
3. **Execution substrate** — runed + `container/` + the egress proxy. The layer
   that PROVIDES the agent environment: home, socket, mounts, network policy.
4. **The agent** — `ant`. Runs inside 3, consumes what 2 defines.

`ant` is a component of nothing. It is not part of `resreg` (it is a CLIENT of
resreg's output — every two-face resource becomes a tool ant calls) and not part
of runed (runed hands it an environment; ant occupies it). It is the first
implementation of layer 4, which is why the component contract above already
names it as the mirror.

The consequence that matters: **a second backend — pi, codex — is a sibling of
`ant`, not of runed.** Writing the slot's contract is the precondition for
backends, for a substrate change, and for shipping the channel layer — one
document, three unblocked items. That document is
[`6/7`](7-environment-contracts.md), which owns both contracts and their
conformance.

A part is shippable exactly when its contract with the neighbouring layer is
statable in one sentence. That is why the substrate ("run a workload with this
home, socket and egress policy"), `resreg` ("one declaration, two faces") and
the channel layer ("this envelope, N platforms") are candidates, and why
`servekit` is not — see below.

## The matrix

All 21 daemons on disk, grouped by verdict. **Surface** = how a
standalone consumer reaches it. **Gate** = the delta to standalone
(empty = already there).

### Core — arizuko-inherent (extraction is a category error)

These _are_ the platform: the folder/JID coordinate, the Turn, the
onboarding domain. Nothing to extract; consumed by the others, not
standalone.

| Daemon  | What it owns                                                  | Why not standalone                                           |
| ------- | ------------------------------------------------------------- | ------------------------------------------------------------ |
| `routd` | Routing + dispatch + turn record; the route table, `routd.db` | The folder/JID model lives here; adapters POST _into_ it     |
| `runed` | Per-turn container spawn/teardown, mounts the group folder    | The Turn primitive bound to the folder-as-agent substrate    |
| `onbod` | Admission queue, invite flows, `onbod.db`                     | Pure arizuko onboarding domain                               |
| `webd`  | The `web` channel — SSE hub, `/chat` `/hook` `/me` `/mcp`     | Registers as the arizuko `web` channel, reads `routd.db`     |
| `dashd` | Operator HTMX console (routes/secrets/grants/tasks/audit)     | ~39 arizuko routes over `routd.db`/`messages.db` + `groups/` |

### Config-driven droppable (standalone daemon — spec exists)

Keep arizuko imports; run generic via TOML/env. These have a standalone
spec and are the credible "drop in front of your stack" story.

| Daemon   | Contract (stranger's promise)                                                 | Surface        | Gate to standalone                                            |
| -------- | ----------------------------------------------------------------------------- | -------------- | ------------------------------------------------------------- |
| `authd`  | OIDC login → mint/verify JWTs; sole signer + JWKs                             | HTTP + JWKs    | `5/1` — split out as the token authority any daemon verifies  |
| `proxyd` | Auth-gated reverse proxy, per-path `public`/`user`/`operator`, runtime routes | Config + `/v1` | `5/7` steps 4–7: strip 4 folder refs, delegate all mint, docs |
| `vited`  | Static `/pub` `/priv` origin (Vite)                                           | HTTP           | 0 internal deps; near-generic (README exists)                 |
| `davd`   | WebDAV workspace over upstream `dufs` + healthcheck                           | HTTP (WebDAV)  | 0 internal deps; thin wrapper, near-generic                   |

### Import-orthogonal components (the toolbox proper)

Sibling dirs / libs consumed with no arizuko process (the component
contract, above).

| Component  | Contract                                           | Surface        | Gate                                                       |
| ---------- | -------------------------------------------------- | -------------- | ---------------------------------------------------------- |
| `crackbox` | Default-deny egress per source id + KVM sandbox    | CLI + HTTP     | **Ships now** — fail-open gate cleared 2026-08-15 (below)  |
| `obs`      | Opt-in `audit_log`+journald+OTLP for any Go daemon | `import` + env | **Ships now** — has a README; needs only a drop-in example |

### Channel + effect/event daemons (correct granularity as daemons)

**Their daemon-ness is the right shape, not a coupling debt.** One
process per platform is the correct granularity: independent failure
(a Slack outage never touches Telegram), independent secrets, independent
deploy/scale, independent reconnect. Each speaks arizuko's message
envelope (`chanlib`) into `routd` — that coupling is _by design_, not
something to file down. The wrong question is "can I ship `slakd` alone?"
(a bridge with no router behind it is inert). The right question, answered
below in _Use-case clusters_, is "does one-daemon-per-channel serve the
deployer's job?" — yes. The channel LAYER is a different question — see
the Package axis below.

| Daemon(s)                                                                | Shape                                          | Daemon-per-X rationale                                 |
| ------------------------------------------------------------------------ | ---------------------------------------------- | ------------------------------------------------------ |
| `slakd` `teled` `discd` `whapd` `bskyd` `reditd` `linkd` `twitd` `emaid` | Platform ↔ `routd` bridge via `chanlib`        | one per platform: isolated failure, secrets, reconnect |
| `timed`                                                                  | Scheduler — injects turns into the bus on cron | one cron plane; a tick is just another Event (`5/A`)   |
| `ttsd`                                                                   | Text→speech HTTP proxy over a TTS backend      | one capability endpoint; near-generic like `vited`     |

## Package axis — import coupling (component candidates)

The daemon matrix asks "deploy alone?"; this asks "import alone?"
Internal-dep counts (indicative — they drift; grep 2026-07-14):

| Package                            | internal deps        | readiness                  | what it is                       |
| ---------------------------------- | -------------------- | -------------------------- | -------------------------------- |
| `crackbox`                         | 0                    | **ships now** (2026-08-15) | egress proxy + KVM sandbox       |
| `obs`                              | 0                    | **ships now**              | tri-substrate observability      |
| `router`                           | 1 (`core`)           | near — lift `core` types   | routing-table DSL                |
| `grants`                           | 2                    | near — needs a store seam  | capability-auth DSL              |
| `resreg`                           | 4                    | roadmap = two-face lib     | two-face API engine (`5/17`)     |
| `dockbox` _(concept)_              | in `container/` (11) | heavy decouple             | Docker sibling to crackbox's KVM |
| store, auth, chanlib, ipc, compose | 3–11                 | **don't extract**          | these _are_ the platform         |

`obs` and `crackbox` ship as-is (crackbox's gate cleared, next
paragraph). `dockbox` names the
run-an-agent-in-a-container concern for a later clean extraction — and that is
where crackbox's `pkg/host` KVM library belongs, which is why nothing imports it
from crackbox today (`6/9`).

**`crackbox`'s ship gate cleared 2026-08-15.** The "egress fails OPEN" blocker
earlier revisions cited was closed: `5/33` grants gate the `*` bypass behind an
explicit `egress` grant, and the e2e suite (Case2/Case4) proves deny. crackbox
is now the first shippable part, not a blocked one. Note it is three things, not
one: a forward proxy with per-source allowlists and a DNS filter (both LIVE and
enforcing — `container/egress.go` registers every spawn's IP with its admin API)
plus the unwired KVM library above. Deleting the mechanism would remove
per-tenant egress enforcement, one of the three claims `USELESS.md` says
survives its own counter; collapsing the component BOUNDARY is a separate and
much cheaper decision.

**`servekit` is retired as a name (2026-08-15).** It bundled three unrelated
contracts by directory and therefore fails the one-sentence test:

- `davd` and `vited` are **zero lines of Go** — davd is `sigoden/dufs` wrapped
  in alpine to add `wget` for a healthcheck (`compose/compose.go:1190`). They
  are already someone else's product, composed.
- `proxyd` (1,591 lines) is the commodity half — Caddy/Traefik plus Authelia's
  job, and the weakest position in the repo. `5/7` already specs it droppable.
- `webd` (3,282 lines) is **not** a web-stack part at all: token chat, SSE,
  `/hook`, `/me` are the agent surface and belong with the platform.

What is irreplaceable there is one mechanism, not a stack: every web surface is
scoped by the same folder and the same `acl` rows as the agent (`auth.MatchSlot`,
per-folder mounts, no parent inheritance — `5/10`). Nextcloud cannot do that
because it has no folder-agent. So the move is **shrink toward off-the-shelf and
publish the grant-scoped mount contract**, not extract a bundle.

**The channel LAYER is near-extractable even though no single adapter is.**
One adapter alone is inert — a bridge with no router — but `chanlib` imports
only `auth`, `db_utils` and `obs`: JWT verification, migrations, logging. **No
`store`, no `core`, no `router`, no `ipc` — zero domain coupling** (`chanreg`
adds `audit` + `core`). The product is "one normalized envelope, N platforms,
with reconnect, declared capabilities and idempotent send"; the gate is
writing the envelope as a contract rather than "whatever routd accepts"
([`6/7`](7-environment-contracts.md) names this as one of its three unblocked
items; the protocol spec is `5/34`).

**`router` ships; routd does not.** `router/` has exactly one internal dep
(`core`) and matching a message against a route table knows nothing about
agents. routd the daemon owns the Turn and the folder coordinate and stays
core — the DSL is agent-agnostic, the daemon is not.

## Reading the matrix — the honest takeaways

- **"Every daemon standalone" is false by design.** 5 core daemons are the
  platform; 10 adapters + `timed`/`ttsd` are coupled bridges. The
  standalone story is **4 config-driven daemons + 2 components**, not 21.
- **Two shelves, not one list.** The import-orthogonal toolbox
  (`obs`/`crackbox` — a stranger _imports_) and the droppable service
  (`5/1`/`5/7`: `authd`/`proxyd` — a stranger _deploys_) are different
  products. Import-count misranks them: `vited`/`davd` are 0-dep but thin;
  `authd`/`proxyd` carry imports yet are the real standalone daemons.
- **Adapter sprawl is correct granularity, not debt** — one daemon per
  channel isolates its own failure; don't consolidate the fleet.
- **No security gate blocks the components.** The egress-fails-open entry
  closed 2026-08-15 (`5/33` grants gate the `*` bypass, e2e proves deny);
  crackbox is shippable now.
- **Ship one, not seven.** `USELESS.md`'s sharpest finding is that arizuko is
  over-built for its proven audience, and "one stranger running one instance is
  worth more than the next five daemons". Carving every boundary in this
  catalogue on spec repeats that in a new register. Ship crackbox, get a
  stranger to run it, and let what they hit choose the next seam.

## Use-case clusters — resolidify by the deployer's job

The verdict tiers above answer "can it stand alone?" The deployer asks a
different question — "which parts do I run for _my_ job?" — and along that
axis the 21 daemons collapse to **8 clusters**. The channel fleet is
_one_ cluster, not ten decisions: that is why "most daemons are adapters"
is a feature, not sprawl.

| Cluster                | Daemons                                                                         | Deployer's job                                | Optionality                     |
| ---------------------- | ------------------------------------------------------------------------------- | --------------------------------------------- | ------------------------------- |
| **Core plane**         | `authd` `routd` `runed`                                                         | run agents at all                             | always required                 |
| **Channel presence**   | `slakd` `teled` `discd` `whapd` `bskyd` `reditd` `linkd` `twitd` `emaid` `webd` | meet users where they are                     | ≥1; add/drop one daemon each    |
| **Web + management**   | `proxyd` `vited` `davd` `dashd`                                                 | put the agent on the web, run/observe/control | shrink to off-the-shelf (above) |
| **Onboarding**         | `onbod`                                                                         | let people join safely                        | optional                        |
| **Scheduling**         | `timed`                                                                         | wake the agent on a clock                     | optional                        |
| **Isolation / egress** | `crackbox`                                                                      | contain what the agent can reach              | optional (security)             |
| **Capabilities**       | `ttsd` (+ Whisper/OpenAI hooks)                                                 | give the agent voice / extra models           | optional                        |
| **Observability**      | `obs` (library)                                                                 | see what happened                             | opt-in, zero-overhead unset     |

This is the **deployer-facing** grouping — orthogonal to the
import-coupling axis (Package axis, above) and the standalone verdict. A minimal
deployment is _Core plane + one Channel_; a maxed one is every cluster.
"One daemon per channel" is the right unit precisely because the deployer
picks channels à la carte, and each fails/scales/reconnects on its own.

## Counterpart landscape — the durable finding

A market sweep (2026-07-15) asked per part whether a real external
counterpart exists. **The finding validates `5/A`: no single primitive is
unique — the composition is the moat.** Only the folder-as-agent org-tree
router is near-unique, and even it has an honest neighbour (OpenClaw has
multiple agents but no arbitrary-depth org-tree + inbox + escalation
ladder). `crackbox` egress and `mcpfw` are early entrants in unsettled
markets; `webd`/`dashd`/channel-fleet are differentiated _bundles_ (the
pieces are buyable, the integrated shape is not); resreg's edge is rigor,
not the idea (FastAPI-MCP ships the idea). Everything else — `authd`
`proxyd` `runed` `timed` `onbod` `ttsd` `obs` and the KVM lib — is
commodity against mature incumbents.

The vendor names behind that reading date fast; re-run the sweep rather
than trusting a stale table. What does not date is the rule it produced:

**Never claim a commodity part is novel** (codex/CTO-audit rule, `5/A`).
"We have an egress proxy" is true and weak; "per-agent folder-scoped
egress bound to agent identity, self-hosted" is the differentiated claim —
and even that ships qualified while `crackbox` is opt-in. Name the
composition, not the part. Perfect the commodities
silently; a landing page that leads with "our OIDC issuer" competes with
Keycloak on Keycloak's turf and loses.

Replacing a native part with its counterpart is possible only for the
commodities, and only **behind arizuko's seam** — never a raw drop-in.
`runed`'s `ContainerRuntime` seam (`5/P`, today Docker + Fake) could carry
an external sandbox, but the folder-mount + MCP-socket contract makes
E2B/Modal a real port. The catch is _can_ ≠ _should_: swapping `authd` for
Keycloak trades a small Go binary for a JVM + its own DB, against the
plain-primitives / one-tar-backup ethos. The differentiated tier has no
such swap — nothing external carries the folder binding.

## What this means for "ship in parts"

Ship on two shelves, not one list:

1. **Components (import)** — `obs` and `crackbox` (both ready), then
   `mcpfw` (`6/12`) and the `resreg` two-face lib as the decouples land
   (ROI order).
2. **Daemons (deploy)** — `authd` + `proxyd` as the "auth gateway you
   drop in front of any stack" pair; `vited`/`davd` fold in as generic
   static/WebDAV origins. `5/1` + `5/7` are the specs; the gate is
   finishing their strip-arizuko steps, not new design.

The core five and the adapter fleet ship only _as arizuko_ — that is the
platform, and `5/A`'s primitives→components→daemons→products stack already
says so: Components is the extraction layer, Daemons is where the platform
lives.

## Ties

`6/1` (adoption — publish the contract) · `6/7` (the two environment
contracts) · `5/1` (`authd` standalone) · `5/7` (`proxyd` standalone) ·
`6/8` (`crackbox` egress) · `6/9` (`crackbox` KVM) · `6/10` (`crackbox` DNS)
· `6/12` (`mcpfw` firewall) · `5/17` (`resreg` two-face) · `5/A`
(primitives framing).
