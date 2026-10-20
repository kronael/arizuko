---
status: shipped
---

# Components & daemons — what ships alone, and against what

The consolidated "ship in parts" spec: which pieces stand alone, on which
of two axes, measured against the market.

## Two standalone axes (do not conflate)

A part can be "standalone" in two different senses; keep them apart.

1. **Import-orthogonal component** — a sibling dir, zero arizuko-internal
   imports, consumed only via CLI / HTTP / `pkg/` (the contract below). The
   _toolbox_ test: raid the repo for one part. Passers today: `mcpfw`
   (`6/12`), `egress` (`6/24`), and `obs`.
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
2. **Does it hold?** — test it adversarially. The egress proxy was the
   lesson: 0 internal deps **and** its default-deny allowlist failed open —
   ready-by-import-count, broken-by-contract.
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
router|groupfolder|ipc|…)` under the component dir → empty
(`tests/components/imports_test.go` runs it on every build). `ant/` is the mirror:
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
   What a substrate can do is declared once, as a value: `substrate.Profile`
   (`container.Profile` for Docker, `runed.qemuProfile` for KVM). This spec
   restates none of it.
4. **The agent** — `ant`. Runs inside 3, consumes what 2 defines.

`ant` is a component of nothing. It is not part of `resreg` (it is a CLIENT of
resreg's output — every two-face resource becomes a tool ant calls) and not part
of runed (runed hands it an environment; ant occupies it). It is the first
implementation of layer 4, which is why the component contract above already
names it as the mirror.

The consequence that matters: **a second backend — pi, codex — is a sibling of
`ant`, not of runed.** Writing the slot's contract is the precondition for
backends, for a substrate change, and for shipping the channel layer. The
harness seam is the shipped answer for the first: `ant/src/backend.ts` owns
the seam types and the selector, and `ARIZUKO_BACKEND` fails loudly on an
unknown name.

A part is shippable exactly when its contract with the neighbouring layer is
statable in one sentence. That is why the substrate ("run a workload with this
home, socket and egress policy"), `resreg` ("one declaration, two faces") and
the channel layer ("this envelope, N platforms") are candidates.

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

| Daemon   | Contract (stranger's promise)                                                 | Surface        | Gate to standalone                                                    |
| -------- | ----------------------------------------------------------------------------- | -------------- | --------------------------------------------------------------------- |
| `authd`  | OIDC login → mint/verify JWTs; sole signer + JWKs                             | HTTP + JWKs    | `5/1` — split out as the token authority any daemon verifies          |
| `proxyd` | Auth-gated reverse proxy, per-path `public`/`user`/`operator`, runtime routes | Config + `/v1` | `5/7` steps 4–7, carried by `6/22` (partial): strip folder refs, docs |
| `vited`  | Static `/pub` `/priv` origin (Vite)                                           | HTTP           | 0 internal deps; near-generic (README exists)                         |
| `davd`   | WebDAV workspace over upstream `dufs` + healthcheck                           | HTTP (WebDAV)  | 0 internal deps; thin wrapper, near-generic                           |

### Import-orthogonal components (the toolbox proper)

Sibling dirs / libs consumed with no arizuko process (the component
contract, above).

| Component | Contract                                                  | Surface        | Gate                                                                     |
| --------- | --------------------------------------------------------- | -------------- | ------------------------------------------------------------------------ |
| `mcpfw`   | Gate an agent's MCP tool calls against a ruleset (`6/12`) | CLI + ruleset  | none — a binary, a README, stub-driven acceptance tests; first in `7/25` |
| `egress`  | Resolve, then check the address, then dial (`6/24`)       | CLI + HTTP     | none — a binary runed starts per spawn; `tests/components` pins the set  |
| `obs`     | Opt-in `audit_log`+journald+OTLP for any Go daemon        | `import` + env | none — has a README; publish on request (`7/25`)                         |

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

| Daemon(s)                                                                       | Shape                                          | Daemon-per-X rationale                                 |
| ------------------------------------------------------------------------------- | ---------------------------------------------- | ------------------------------------------------------ |
| `slakd` `teled` `discd` `whapd` `bskyd` `reditd` `linkd` `twitd` `emaid` `gitd` | Platform ↔ `routd` bridge via `chanlib`        | one per platform: isolated failure, secrets, reconnect |
| `timed`                                                                         | Scheduler — injects turns into the bus on cron | one cron plane; a tick is just another Event (`5/A`)   |
| `ttsd`                                                                          | Text→speech HTTP proxy over a TTS backend      | one capability endpoint; near-generic like `vited`     |

## Package axis — import coupling (component candidates)

The daemon matrix asks "deploy alone?"; this asks "import alone?"
Direct non-test arizuko imports, measured 2026-09-05 (`grep` of
`github.com/kronael/arizuko/` per package; `tests/components` pins the
shippable set):

| Package                  | internal deps                                                                         | readiness                                     | what it is                                                                                               |
| ------------------------ | ------------------------------------------------------------------------------------- | --------------------------------------------- | -------------------------------------------------------------------------------------------------------- |
| `mcpfw`, `egress`, `obs` | 0                                                                                     | **ship now** (`7/25` order)                   | MCP gate, egress proxy, observability                                                                    |
| `substrate`              | 0                                                                                     | **ship now**                                  | the capability declaration (`Profile`)                                                                   |
| `groupfolder`            | 0                                                                                     | not published — 73 callers                    | the containment predicate (`7/25`)                                                                       |
| `router`                 | 1 (`core`)                                                                            | near — lift `core` types                      | routing-table DSL                                                                                        |
| `resreg`                 | 5 (`audit`, `auth`, `core`, `objstore`, `store`)                                      | roadmap = two-face lib                        | two-face API engine (`5/17`)                                                                             |
| `chanlib`                | 7 (`auth`, `chanlib/chanstore`, `core`, `objstore/dynstore`, `obs`, `store`, `theme`) | far — the store is the cost, not the envelope | the channel layer (`5/34`)                                                                               |
| `auth`                   | 4 (`core`, `groupfolder`, `store`, `theme`)                                           | **don't extract**                             | the grant evaluator reads `acl` via `store`                                                              |
| `container`              | 3 (`chanlib`, `core`, `groupfolder`)                                                  | **don't extract**                             | group setup + the egress rule translation; the Docker substrate it was named for is gone (`6/41` step 4) |
| store, ipc, compose      | 3–9                                                                                   | **don't extract**                             | these _are_ the platform                                                                                 |

The Docker substrate is `container/` and its declaration is
`runed.qemuProfile`; the KVM substrate is unexported code inside `runed/`
and its declaration is `runed.qemuProfile`
([`6/41`](41-qemu-substrate.md)). What either can do is read from those
values (`6/25` §2), not from this table. Neither is a component: a
substrate is runed's own mechanism.

**The web stack is three unrelated contracts**, so as a bundle it fails the
one-sentence test:

- `davd` and `vited` are **zero lines of Go** — davd is `sigoden/dufs` wrapped
  in alpine to add `wget` for a healthcheck (`compose/compose.go:1190`). They
  are already someone else's product, composed.
- `proxyd` (1,768 lines) is the commodity half — Caddy/Traefik plus Authelia's
  job, and the weakest position in the repo. `5/7` already specs it droppable.
- `webd` (3,364 lines) is **not** a web-stack part at all: token chat, SSE,
  `/hook`, `/me` are the agent surface and belong with the platform.

What is irreplaceable there is one mechanism, not a stack: every web surface is
scoped by the same folder and the same `acl` rows as the agent (`auth.MatchSlot`,
per-folder mounts, no parent inheritance — `SECURITY.md` § The public and private web trees are separate). Nextcloud cannot do that
because it has no folder-agent. So the move is **shrink toward off-the-shelf and
publish the grant-scoped mount contract**, not extract a bundle.

**The channel LAYER is FAR from extractable, and the reason moved.** One adapter alone
is inert — a bridge with no router — and `chanlib` now imports `auth`,
`chanlib/chanstore`, `core`, `objstore/dynstore`, `obs`, `store` and `theme`
(measured 2026-10-08, production-only direct imports). This section claimed `auth`,
`db_utils`, `obs`, `theme` and **"no `store`, no `core`, zero domain coupling"**;
`db_utils` no longer exists and `store` plus `objstore/dynstore` are now direct. The
store arrived under it with `chanstore` and the dedup ledger, so the gate is no longer
writing the envelope as a contract — it is that the layer holds its own tables. The product is "one normalized envelope, N platforms,
with reconnect, declared capabilities and idempotent send"; the gate is
writing the envelope as a contract rather than "whatever routd accepts"
(the protocol spec is `5/34`).

**`router` ships; routd does not.** `router/` has exactly one internal dep
(`core`) and matching a message against a route table knows nothing about
agents. routd the daemon owns the Turn and the folder coordinate and stays
core — the DSL is agent-agnostic, the daemon is not.

## Reading the matrix — the honest takeaways

- **"Every daemon standalone" is false by design.** 5 core daemons are the
  platform; 10 adapters + `timed`/`ttsd` are coupled bridges. The
  standalone story is **4 config-driven daemons + 2 components**, not 21.
- **Two shelves, not one list.** The import-orthogonal toolbox
  (`obs`/`mcpfw` — a stranger _imports_) and the droppable service
  (`5/1`/`5/7`: `authd`/`proxyd` — a stranger _deploys_) are different
  products. Import-count misranks them: `vited`/`davd` are 0-dep but thin;
  `authd`/`proxyd` carry imports yet are the real standalone daemons.
- **Adapter sprawl is correct granularity, not debt** — one daemon per
  channel isolates its own failure; don't consolidate the fleet.
- **No security gate blocks the components.** `5/33` grants gate the `*`
  egress bypass behind an explicit `egress` grant, and the e2e suite
  (Case2/Case4) proves deny; the egress proxy is shippable.
- **Ship one, not seven.** `USELESS.md`'s sharpest finding is that arizuko is
  over-built for its proven audience, and "one stranger running one instance is
  worth more than the next five daemons". Carving every boundary in this
  catalogue on spec repeats that in a new register. Ship `mcpfw` (`7/25`),
  get a stranger to run it, and let what they hit choose the next seam.

## Use-case clusters — resolidify by the deployer's job

The verdict tiers above answer "can it stand alone?" The deployer asks a
different question — "which parts do I run for _my_ job?" — and along that
axis the 21 daemons collapse to **8 clusters**. The channel fleet is
_one_ cluster, not ten decisions: that is why "most daemons are adapters"
is a feature, not sprawl.

| Cluster                | Daemons                                                                                | Deployer's job                                | Optionality                     |
| ---------------------- | -------------------------------------------------------------------------------------- | --------------------------------------------- | ------------------------------- |
| **Core plane**         | `authd` `routd` `runed`                                                                | run agents at all                             | always required                 |
| **Channel presence**   | `slakd` `teled` `discd` `whapd` `bskyd` `reditd` `linkd` `twitd` `emaid` `gitd` `webd` | meet users where they are                     | ≥1; add/drop one daemon each    |
| **Web + management**   | `proxyd` `vited` `davd` `dashd`                                                        | put the agent on the web, run/observe/control | shrink to off-the-shelf (above) |
| **Onboarding**         | `onbod`                                                                                | let people join safely                        | optional                        |
| **Scheduling**         | `timed`                                                                                | wake the agent on a clock                     | optional                        |
| **Isolation / egress** | `egress`                                                                               | contain what the agent can reach              | optional (security)             |
| **Capabilities**       | `ttsd` (+ Whisper/OpenAI hooks)                                                        | give the agent voice / extra models           | optional                        |
| **Observability**      | `obs` (library)                                                                        | see what happened                             | opt-in, zero-overhead unset     |

This is the **deployer-facing** grouping — orthogonal to the
import-coupling axis (Package axis, above) and the standalone verdict. A minimal
deployment is _Core plane + one Channel_; a maxed one is every cluster.
"One daemon per channel" is the right unit precisely because the deployer
picks channels à la carte, and each fails/scales/reconnects on its own.

## Counterpart landscape — measured 2026-09-05

Per commodity part: the incumbent a stranger adopts, and whether it
carries the folder coordinate. Every row is from the incumbent's docs or
source; the one README-only row is marked. The orchestration field —
who else routes agents across tenants — is not measured here.

| Part     | Incumbent to adopt                                                           | License                 | Carries the folder coordinate?                                                                                                                                                                                                                                                             |
| -------- | ---------------------------------------------------------------------------- | ----------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `authd`  | Keycloak (heavy); Zitadel, Ory Hydra, Dex (light)                            | Apache-2.0              | **the coordinate, yes**: a Keycloak group is a slash-separated path, subgroups inherit role mappings, and a mapper puts the path into the token (keycloak.org server_admin, "Groups"). Not the binding.                                                                                    |
| `proxyd` | Pomerium                                                                     | Apache-2.0              | **mostly**: an identity-aware proxy whose policy decides route + authorization and whose authorization service "constructs security headers based on JWT claims" for upstreams (pomerium.com/docs/internals/architecture) — proxyd's transit stamp, with a group claim for the path ladder |
| `runed`  | kubernetes-sigs `agent-sandbox`; Daytona; Anthropic self-hosted sandboxes    | Apache-2.0; AGPL-3.0; — | no: image + workspace in, sandbox out; none derives an egress allowlist from a path. They are occupants for the `runed.Runtime` seam (`runed/runtime.go:40` — `Run` + `Kill`), not replacements for the folder mount contract.                                                             |
| `timed`  | `robfig/cron`, `river`; Temporal Schedules at the heavy end                  | MIT                     | no: the only arizuko part is that a tick becomes an Event on the bus with a folder                                                                                                                                                                                                         |
| `onbod`  | Zitadel organizations + registration; Ory Kratos self-service flows          | Apache-2.0              | no: an org is a flat tenant ("comparable to a tenant", zitadel.com/docs/concepts/structure/organizations); admission into a subtree has no carrier                                                                                                                                         |
| `ttsd`   | `speaches-ai/speaches` — OpenAI-compatible TTS and STT, self-hosted (README) | MIT                     | there is no tenancy dimension to carry; the cheapest swap in the repo, behind the HTTP seam that already exists                                                                                                                                                                            |
| `obs`    | OpenTelemetry Go SDK + `otelslog` bridge + Collector                         | Apache-2.0              | **split**: the slog→OTLP fan-out is exactly `go.opentelemetry.io/contrib/bridges/otelslog`, and a `folder` attribute rides any OTel record; the `audit_log` row that the ACL evaluator and the cost gate read back has no external system of record                                        |

**The finding.** Three of the seven commodity parts have an external
carrier for the coordinate — Keycloak carries the path, Pomerium the
path-scoped gate, OTel the attribute. None carries the **binding**: one
path that is at once tenant, ACL scope, route target, egress allowlist,
web host and file tree, applied by one evaluator to the agent over MCP
and the human over REST. That composition is the claim. A part is not,
and the rule stands: **never claim a commodity part is novel** (`5/A`).
"We have an OIDC issuer" competes with Keycloak on Keycloak's turf and
loses; "one path binds six things" is the sentence, and even it ships
qualified while per-spawn egress is opt-in.

Replacing a native part with its counterpart is possible only for the
commodities, and only behind arizuko's seam — never a raw drop-in.
`runed`'s `Runtime` port (`5/P`; `runed/runtime.go:70`) can carry an
external sandbox, but the folder-mount + MCP-socket contract makes it a
port, not a swap. The catch is _can_ ≠ _should_: swapping `authd` for
Keycloak trades a small Go binary for a JVM and its own database,
against the plain-primitives / one-tar-backup ethos. `ttsd` is the one
part where the swap is free.

## What is settled, and what is not

Settled, and measured: the two axes; the four layers; the per-package
import graph (pinned by `tests/components`); the verdict per daemon; the
counterpart per commodity part. Nothing here is code to write, so the
document's lifecycle is that of an analysis, not a feature: it is
complete as an inventory.

Two rows still move with other specs. The `proxyd` gate is carried by
`6/22` (partial), and the substrate rows follow `substrate.Profile`, which
`6/25` owns. Vendor names date; re-measure a row before citing
it.

## What this means for "ship in parts"

Ship on two shelves, not one list:

1. **Components (import)** — `mcpfw`, then `egress`, then `obs` on
   request (`7/25`'s order); the `resreg` two-face lib only after its three
   imports are cut.
2. **Daemons (deploy)** — `authd` + `proxyd` as the "auth gateway you
   drop in front of any stack" pair; `vited`/`davd` fold in as generic
   static/WebDAV origins. `5/1` + `5/7` are the specs; the gate is
   finishing their strip-arizuko steps, not new design.

The core five and the adapter fleet ship only _as arizuko_ — that is the
platform, and `5/A`'s primitives→components→daemons→products stack already
says so: Components is the extraction layer, Daemons is where the platform
lives.

## Ties

`ARCHITECTURE.md` § Decided, and why (the two published environments) · `5/1` (`authd` standalone) · `5/7` (`proxyd` standalone) ·
`6/24` (`egress` per-spawn proxy) · [`6/41`](41-qemu-substrate.md) (the VM
substrate, which makes the KVM half runed's own code)
· `6/12` (`mcpfw` firewall) · `5/17` (`resreg` two-face) · `5/A`
(primitives framing).
