---
status: draft
---

# Components & daemons — what ships alone, and against what

The consolidated "ship in parts" spec: which pieces stand alone, on which
of two axes, measured against the market. Absorbs the former package
catalogue and extraction method; `6/7` remains the definitional pattern
this applies.

## Two standalone axes (do not conflate)

A part can be "standalone" in two different senses; keep them apart.

1. **Import-orthogonal component** (`6/7`) — a sibling dir, zero
   arizuko-internal imports, consumed only via CLI / HTTP / `pkg/`. The
   _toolbox_ test: raid the repo for one part. Passers today: `crackbox`,
   `obs`; roadmap: `mcpfw` (←`resreg`), `gateway` (←messaging), `sandd`.
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
2. **Does it hold?** — test it adversarially. `crackbox` is the lesson: 0
   internal deps **and** its default-deny egress fails open (`BUGS.md`) —
   ready-by-import-count, broken-by-contract.
3. **Stranger vs cruft** — the minimal surface a stranger imports/runs,
   minus every folder/JID/`routd.db` assumption baked in. That delta is
   the extraction work.

## The matrix

All 22 daemons on disk, grouped by verdict. **Surface** = how a
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

| Daemon   | Contract (stranger's promise)                            | Surface        | Gate to standalone                                            |
| -------- | -------------------------------------------------------- | -------------- | ------------------------------------------------------------- |
| `authd`  | OIDC login → mint/verify JWTs; sole signer + JWKs        | HTTP + JWKs    | `5/1` — split out as the token authority any daemon verifies  |
| `proxyd` | Auth-gated reverse proxy, per-path tiers, runtime routes | Config + `/v1` | `5/7` steps 4–7: strip 4 folder refs, delegate all mint, docs |
| `vited`  | Static `/pub` `/priv` origin (Vite)                      | HTTP           | 0 internal deps; near-generic already, needs only a README    |
| `davd`   | WebDAV workspace over upstream `dufs` + healthcheck      | HTTP (WebDAV)  | 0 internal deps; thin wrapper, near-generic                   |

### Import-orthogonal components (the toolbox proper)

Sibling dirs / libs consumed with no arizuko process. The `6/7` pattern.

| Component          | Contract                                           | Surface        | Gate                                                    |
| ------------------ | -------------------------------------------------- | -------------- | ------------------------------------------------------- |
| `crackbox`/`egred` | Default-deny egress per source id + KVM sandbox    | CLI + HTTP     | **egress fails OPEN** (BUGS HIGH) — the real ship gate  |
| `obs`              | Opt-in `audit_log`+journald+OTLP for any Go daemon | `import` + env | **Ships now.** Needs only a standalone README + example |

### Channel + effect/event daemons (correct as daemons — not extraction candidates)

**Their daemon-ness is the right shape, not a coupling debt.** One
process per platform is the correct granularity: independent failure
(a Slack outage never touches Telegram), independent secrets, independent
deploy/scale, independent reconnect. Each speaks arizuko's message
envelope (`chanlib`) into `routd` — that coupling is _by design_, not
something to file down. The wrong question is "can I ship `slakd` alone?"
(a bridge with no router behind it is inert). The right question, answered
below in _Use-case clusters_, is "does one-daemon-per-channel serve the
deployer's job?" — yes.

| Daemon(s)                                                                        | Shape                                          | Daemon-per-X rationale                                 |
| -------------------------------------------------------------------------------- | ---------------------------------------------- | ------------------------------------------------------ |
| `slakd` `teled` `discd` `whapd` `mastd` `bskyd` `reditd` `linkd` `twitd` `emaid` | Platform ↔ `routd` bridge via `chanlib`        | one per platform: isolated failure, secrets, reconnect |
| `timed`                                                                          | Scheduler — injects turns into the bus on cron | one cron plane; a tick is just another Event (`5/A`)   |
| `ttsd`                                                                           | Text→speech HTTP proxy over a TTS backend      | one capability endpoint; near-generic like `vited`     |

## Package axis — import coupling (component candidates)

The daemon matrix asks "deploy alone?"; this asks "import alone?"
Internal-dep counts (grep over each package's imports, 2026-07-14):

| Package                            | internal deps          | readiness                  | what it is                       |
| ---------------------------------- | ---------------------- | -------------------------- | -------------------------------- |
| `crackbox`                         | 0                      | ⚠ egress fails open (HIGH) | egress proxy + KVM sandbox       |
| `obs`                              | 0                      | **ships now**              | tri-substrate observability      |
| `router`                           | 1 (`core`)             | near — lift `core` types   | routing-table DSL                |
| `grants`                           | 2                      | near — needs a store seam  | capability-auth DSL              |
| `resreg`                           | 4                      | roadmap = `mcpfw`          | two-face API engine (`5/17`)     |
| `dockbox` _(concept)_              | in `container/` (11)   | heavy decouple             | Docker sibling to crackbox's KVM |
| `servekit` _(concept)_             | proxyd/webd/dashd 8–13 | heavy decouple             | the web+service stack (below)    |
| store, auth, chanlib, ipc, compose | 3–11                   | **don't extract**          | these _are_ the platform         |

Only `obs` plausibly ships as-is; `crackbox` is 0-dep **and** broken (the
rubric's point — import-purity ≠ readiness). `servekit` names the
web+management bundle (`proxyd` reverse proxy + `webd` token chat/SSE/
webhooks + `vited`/`davd` static/WebDAV + `dashd` console) — deeply
arizuko-coupled today (folder identity, `routd.db`, `groups/` FS), a
decouple-and-name candidate, not a standalone. `dockbox` names the
run-an-agent-in-a-container concern for a later clean extraction.

## Reading the matrix — the honest takeaways

- **"Every daemon standalone" is false by design.** 5 core daemons are
  the platform (category error to extract), 10 adapters + `timed`/`ttsd`
  are coupled bridges. The standalone story is \*\*4 config-driven daemons
  - 2 components\*\*, not 22.
- **The toolbox (`6/7`, zero-import) and the droppable-service (`5/1`,
  `5/7`) stories are different products.** `obs`/`crackbox` are libraries
  a stranger imports; `proxyd`/`authd` are daemons a stranger deploys.
  Present them on different shelves.
- **Import-purity ranks the wrong daemons.** `vited`/`davd` are 0-dep but
  low-value thin wrappers; `authd`/`proxyd` carry imports yet are the real
  standalone daemons. Coupling count is a filter, not the verdict.
- **One security gate blocks the only sure component.** `crackbox`'s
  egress fails open (`BUGS.md` 2026-07-14) — the ship gate is the fix, not
  a README.
- **Adapter sprawl is correct granularity, not debt.** Most daemons are
  channel adapters _because_ one-daemon-per-channel is the right unit —
  the deployer adds channels à la carte and each isolates its own
  failure. Don't consolidate the fleet; that coupling is the design.

## Use-case clusters — resolidify by the deployer's job

The verdict tiers above answer "can it stand alone?" The deployer asks a
different question — "which parts do I run for _my_ job?" — and along that
axis the 22 daemons collapse to **8 clusters**. The channel fleet is
_one_ cluster, not ten decisions: that is why "most daemons are adapters"
is a feature, not sprawl.

| Cluster                | Daemons                                                                                 | Deployer's job                                | Optionality                   |
| ---------------------- | --------------------------------------------------------------------------------------- | --------------------------------------------- | ----------------------------- |
| **Core plane**         | `authd` `routd` `runed`                                                                 | run agents at all                             | always required               |
| **Channel presence**   | `slakd` `teled` `discd` `whapd` `mastd` `bskyd` `reditd` `linkd` `twitd` `emaid` `webd` | meet users where they are                     | ≥1; add/drop one daemon each  |
| **Web + management**   | `proxyd` `vited` `davd` `dashd`                                                         | put the agent on the web, run/observe/control | the _servekit_ bundle (above) |
| **Onboarding**         | `onbod`                                                                                 | let people join safely                        | optional                      |
| **Scheduling**         | `timed`                                                                                 | wake the agent on a clock                     | optional                      |
| **Isolation / egress** | `crackbox`/`egred`                                                                      | contain what the agent can reach              | optional (security)           |
| **Capabilities**       | `ttsd` (+ Whisper/OpenAI hooks)                                                         | give the agent voice / extra models           | optional                      |
| **Observability**      | `obs` (library)                                                                         | see what happened                             | opt-in, zero-overhead unset   |

This is the **deployer-facing** grouping — orthogonal to the
import-coupling axis (Package axis, above) and the standalone verdict. A minimal
deployment is _Core plane + one Channel_; a maxed one is every cluster.
"One daemon per channel" is the right unit precisely because the deployer
picks channels à la carte, and each fails/scales/reconnects on its own.

## Counterpart landscape (market sweep, 2026-07-15)

A four-bucket web sweep asked, per part: does a real external counterpart
exist, how crowded is it, and is arizuko's angle a commodity or an edge?
**The finding validates `5/A`: no single primitive is unique — the
composition is the moat.** Only the folder-as-agent router survives as
near-unique, and even it has an honest neighbor (OpenClaw). Everything
else is commodity or a _differentiated bundle_ (the pieces are buyable;
the integrated shape is not).

| Part                | External category                        | Counterparts (nearest named)                            | Maturity          | Verdict                         |
| ------------------- | ---------------------------------------- | ------------------------------------------------------- | ----------------- | ------------------------------- |
| `routd`             | folder-as-agent org-tree router          | OpenClaw (flat, no tree); LangGraph/AutoGen (in-proc)   | EMERGING          | **DIFFERENTIATED (near-uniq)**  |
| `crackbox`/`egred`  | per-agent default-deny egress            | Stripe **smokescreen** (generic); Safeguard, Pipelock   | MATURE / emerging | **DIFFERENTIATED**              |
| `mcpfw` (←`resreg`) | MCP tool-call firewall                   | **Invariant Gateway**, **Docker MCP Gateway**           | EMERGING          | DIFFERENTIATED, not unique      |
| resreg two-face     | REST+MCP+OpenAPI from one handler        | **FastAPI-MCP** (direct hit); Speakeasy/Stainless       | EMERGING          | DIFF — edge is _rigor_ not idea |
| channel fleet       | agent-first omni-channel bridge          | Matterbridge, Matrix/mautrix, Chatwoot (all human/CX)   | MATURE / emerging | **DIFFERENTIATED**              |
| `webd`              | agent web presence (widget+SSE+hook+MCP) | Svix, Mercure, mcp-proxy (per-piece, no bundle)         | EMERGING (bundle) | **DIFFERENTIATED**              |
| `dashd`             | self-hosted agent-ops console            | Langfuse/Helicone (obs half); Agent 365/Bedrock (cloud) | EMERGING          | **DIFFERENTIATED**              |
| `authd`             | OIDC/JWT issuer                          | Keycloak, Ory Hydra, Dex, Zitadel, Authentik            | MATURE            | COMMODITY                       |
| `proxyd`            | auth-gated reverse proxy                 | oauth2-proxy, Pomerium, Ory Oathkeeper, Authelia        | MATURE            | COMMODITY (lightly diff)        |
| `runed`             | ephemeral agent sandbox                  | E2B, Modal, Daytona, Cloudflare Sandbox, Fly Machines   | MATURE            | COMMODITY                       |
| crackbox KVM lib    | microVM sandbox library                  | Firecracker, Kata, libkrun, gVisor                      | MATURE (dormant)  | COMMODITY                       |
| `timed`             | cron → agent turn                        | Temporal, Inngest, Trigger.dev, Cloudflare Cron         | MATURE            | COMMODITY                       |
| `onbod`             | multi-tenant onboarding                  | WorkOS, Clerk, Scalekit                                 | MATURE            | COMMODITY                       |
| `ttsd`              | TTS proxy                                | ElevenLabs, OpenAI TTS, Cartesia, Piper                 | MATURE            | COMMODITY                       |
| `obs`               | OTel + agent tracing                     | OTel/Collector, Langfuse, Helicone, OpenLLMetry         | MATURE            | COMMODITY                       |

### Promote vs perfect — the strategy

Three tiers decide where marketing air goes. **Perfect all; promote the
top two.**

1. **Lead with the composition (near-unique).** The whole — a
   self-hosted multi-tenant **folder-as-agent org-tree router** bundling
   per-turn ephemeral folder-mounted containers + per-agent default-deny
   egress + an MCP firewall + a multi-channel fleet — is what no named
   competitor packages together. `routd` + the org-tree/inbox/escalation
   substrate is the one piece with no clean equivalent (OpenClaw is flat,
   the frameworks are in-process DAGs). This IS the `5/A` hero story; the
   sweep confirms it is honestly defensible.
2. **Promote the differentiated bundles + new-market bets.** `crackbox`
   per-agent egress and `mcpfw` are early entrants in genuinely _unsettled_
   markets (agent egress, MCP security — both <18 months old) — promote as
   category bets, not solved commodities. `webd`/`dashd`/channel-fleet are
   integration edges: the shape isn't sold as a unit. resreg's edge is
   **rigor** (reflection-OpenAPI, param-bound folder gate), not the idea
   (FastAPI-MCP ships the idea) — pitch the rigor, don't claim first-of-kind.
3. **Perfect the commodities silently — never lead with them.** `authd`
   `proxyd` `runed` `timed` `onbod` `ttsd` `obs` + the KVM lib sit in
   crowded, mature markets. Match table-stakes and move on; a landing page
   that leads "our OIDC issuer" or "our TTS proxy" competes against
   Keycloak and ElevenLabs on their turf and loses. Their job is to make
   the composition work, not to be sold.

Honest caveat for the docs: never claim a commodity part is novel
(codex/CTO-audit rule, `5/A`). "We have an egress proxy" is true and weak;
"per-agent default-deny egress bound to the agent's identity, self-hosted"
is the differentiated claim. Name the composition, not the part.

### Replace a native part with its counterpart?

Only the commodities, and only **behind arizuko's seam** — never a raw
drop-in. `runed` slots an external sandbox (E2B/Modal) behind its
`ContainerRuntime` seam (`5/P`); `authd`, a thin sole-signer, can defer to
Keycloak/Dex if it mints folder/tier claims + JWKs; `timed`/`ttsd`/`obs`
are already thin proxies over swappable backends. The catch: _can_ ≠
_should_ — swapping `authd` for Keycloak trades a small Go binary for a
JVM + its own DB, against the plain-primitives / one-tar-backup ethos. The
native parts stay minimal precisely so you never have to reach for the
heavyweight. The differentiated/near-unique tier has no such swap: nothing
external carries the folder binding.

## What this means for "ship in parts"

Ship on two shelves, not one list:

1. **Components (import)** — `obs` (ready, needs README), `crackbox`
   (needs the fail-closed fix first), then `mcpfw`/`gateway`/`sandd` as
   the decouples land (ROI order).
2. **Daemons (deploy)** — `authd` + `proxyd` as the "auth gateway you
   drop in front of any stack" pair; `vited`/`davd` fold in as generic
   static/WebDAV origins. `5/1` + `5/7` are the specs; the gate is
   finishing their strip-arizuko steps, not new design.

The core five and the adapter fleet ship only _as arizuko_ — that is the
platform, and the four-layer stack (`5/A`) already says so: Components is
the extraction layer, Daemons is where the platform lives.

## Ties

`6/7` (what an import-orthogonal component is — the pattern this applies) ·
`6/1` (adoption / wrap-the-harness) · `5/1` (`authd` standalone) · `5/7`
(`proxyd` standalone) · `6/8` (`crackbox`) · `5/17` (`resreg` two-face →
`mcpfw`) · `5/A` (four-layer stack) · `BUGS.md` (crackbox fail-open — the
ship gate).
