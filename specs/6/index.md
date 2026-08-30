---
status: active
---

# specs/6 — split arizuko into parts

The end goal is that people run arizuko instead of their own agent
harness. The path (thesis inverted 2026-08-15, `6/1`): **publish the
environment, do not wrap the harness.** Like Lambda, arizuko publishes
contracts — the agent environment and the component environment — and
the value is what the environment gives you for free: tenancy, egress,
ACL, web, audit. [`7-environment-contracts.md`](7-environment-contracts.md)
is the spine: it states both contracts and their conformance (agent:
`anteval` per backend, `pi` the intended second occupant; component: a
probe against a live process). Wrapping a foreign harness stays an
on-ramp; emulation is rejected.

**The phase also carries the operator cockpit** (`28`–`33`, moved here from
phase 7 on 2026-08-27). It belongs with the parts, not beside the cloud: every
daemon serving its own `/dash/<daemon>/` from its own `/v1` is what makes a
daemon observable and controllable ALONE, which is the same property `16`
measures and `7` states as a contract. A part nobody can watch has not shipped.

## Specs

| Spec                                                               | Status  | Covers                                                                                                                                                                                                                                                                 |
| ------------------------------------------------------------------ | ------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| [7-environment-contracts.md](7-environment-contracts.md)           | draft   | **the spine**: the agent contract (identity, home, envelope, idempotent delivery, unfrozen lifecycle seam) + the component contract; conformance = `anteval` per backend / a live probe                                                                                |
| [1-adoption-interop.md](1-adoption-interop.md)                     | draft   | adoption strategy: publish-the-contract thesis (Lambda analogy), the wrap demoted to on-ramp, the agentic reimplementation loop, the seven services, demand-class mode                                                                                                 |
| [16-daemon-standalone-matrix.md](16-daemon-standalone-matrix.md)   | draft   | the four-layer model (surfaces / core / substrate / agent — `ant` is the first layer-4 occupant) + two standalone axes + the matrices; `servekit` retired; crackbox's ship gate cleared                                                                                |
| [9-crackbox-sandboxing.md](9-crackbox-sandboxing.md)               | shipped | the KVM/qemu sandbox library, now `sandbox/` — **shipped but unwired**: `runed` still spawns Docker; its consumer is `crackbox run --kvm` (was `12/12`)                                                                                                                |
| [12-mcp-firewall.md](12-mcp-firewall.md)                           | draft   | `mcpfw` — transparent MCP proxy for THIRD-PARTY MCP servers; deny-wins tool-call filter (was `12/17`)                                                                                                                                                                  |
| [18-durable-workflows-decline.md](18-durable-workflows-decline.md) | shipped | costed DECLINE of a durable-execution engine — `5/A` decision 5 stands; reopen trigger + the four-table shape to copy if it fires                                                                                                                                      |
| [19-unified-tool-declaration.md](19-unified-tool-declaration.md)   | draft   | collapse `[[ext]]`+`[[mcp_connector]]` into one `[[connector]]` block + the ordered-source shadow rule; timed to `5/13` shape 4                                                                                                                                        |
| [20-sandbox-lifecycle-decline.md](20-sandbox-lifecycle-decline.md) | shipped | costed DECLINE of the warm-pool sandbox lifecycle vs per-turn ephemeral containers; the mount-boundary blocker; `can_open_io`+reconcile survive for `6/9` KVM                                                                                                          |
| [22-proxyd-cut.md](22-proxyd-cut.md)                               | draft   | cut proxyd to its irreplaceable half — move the misfiled token handlers out (step 1, standalone), routes as rows (step 2, `L45`), forward-auth split (step 3, unsigned). Does NOT remove proxyd                                                                        |
| [23-llm-gateway.md](23-llm-gateway.md)                             | draft   | adopt Bifrost so the container holds a per-spawn virtual key, not the real model credential; cost measured at the boundary instead of self-reported by the agent                                                                                                       |
| [24-crackbox-lightest.md](24-crackbox-lightest.md)                 | shipped | cut crackbox to one static per-spawn forward proxy (~250-300 lines): no registry, no admin API, no transparent mode, no SNI peek. Splits into `egress/` (what runed runs), `sandbox/` (the KVM library) and `crackbox/` — a LOCAL DEV command, not a shipped component |
| [25-qm-adoptions.md](25-qm-adoptions.md)                           | draft   | four decisions read out of `yc-software/qm` (MIT, 83k LOC): what arizuko takes, what it declines, and why the composition stays the moat                                                                                                                               |
| [26-personal-mode.md](26-personal-mode.md)                         | draft   | one human, one terminal, one approval per call: the no-adapter topology, `arizuko talk` over `/chat/<t>/mcp`, GitHub as a connector, MITM vs HITL, and the four verdicts (yes/no/always/never) as `acl` rows — needs deny-wins in `CheckHold`                          |
| [27-git-channel.md](27-git-channel.md)                             | draft   | `gitd` — a Git host as an ingress-only channel: webhooks → `git:` JIDs, PR/issue = topic, `send_text` the only verb; egress stays the GitHub connector (moved from `17/1` 2026-08-25)                                                                                  |
| [28-cockpit-index.md](28-cockpit-index.md)                         | draft   | **the cockpit anchor**: architecture, the `/v1`-only read path, routing, auth, theme, non-goals — every other cockpit spec points back at it (was `7/1`)                                                                                                               |
| [29-dashd-hub.md](29-dashd-hub.md)                                 | draft   | the `dashd` hub + the cross-cutting operator pages that stay central (was `7/2`)                                                                                                                                                                                       |
| [30-per-daemon-dashboards.md](30-per-daemon-dashboards.md)         | partial | routd, runed, authd, proxyd, onbod, timed, crackbox, webd, davd, ttsd — one table plus the per-daemon reasoning that survives (was `7/3`)                                                                                                                              |
| [31-adapter-contract.md](31-adapter-contract.md)                   | draft   | the shared adapter dashboard grammar + health model, defined once (was `7/11`)                                                                                                                                                                                         |
| [32-adapter-dashboards.md](32-adapter-dashboards.md)               | draft   | the ten channel adapters instantiating that contract (was `7/12`)                                                                                                                                                                                                      |
| [33-social-adapter-model.md](33-social-adapter-model.md)           | draft   | the boundary between social adapters (asymmetric visibility, selective engagement) and chat adapters (bidirectional, push) (was `7/41`)                                                                                                                                |

`7` owns the contracts; `1` owns the strategy; `16` owns the map (layers,
axes, matrices); `28` owns the cockpit and every one of `29`–`33` points back
at it. `9` (crackbox KVM), `24` (the per-spawn egress proxy) and
`12` (`mcpfw`) are the concrete component specs from the former phase 12. `17`–`20` are the
**Centaur peel** (`paradigmxyz/centaur@20f3021`, Apache-2.0, read
2026-08-10): `18` and `20` shipped as decision records (decline, costed,
with reopen triggers); `17` was narrowed twice on 2026-08-15 (signing →
request tool; vendor-signing transforms declined), then declined and
deleted 2026-08-20 (see the deleted table); `19` is timed, not
standalone.

## The plan from here

The phase is `active` (2026-08-15). What ships, in the order the specs
themselves argue:

1. **The `6/7` contracts made real** — the component probe **shipped
   2026-08-17** (`arizuko probe <base-url>`); routd and proxyd pass 5/5
   live, dashd 3/3. pi's session resume **shipped 2026-08-27**, so all three
   backends now resume layer 1. **codex is live-certified the same day: 7/9 on
   the smoke gate.** Getting there took two fixes — `ARIZUKO_BACKEND` never
   reached runed (`L128`), so no backend could be certified at all, and the
   backend switch leaves a claude model id behind (`L129`), so every codex turn
   400d until the model was set too. `pi` is blocked on a credential, not on
   code: both credentials the host holds are refused by their own APIs. The
   remaining work is diagnosing codex's two failures, which share one
   signature, and an operator decision on pi's provider key.
2. **The opaque-client credential hole is closed by declining it (2026-08-20)** —
   arizuko serves credentials through one mechanism, the MCP broker; an opaque
   in-container client is out of scope by decision, not by coverage. The spec
   that proposed filling it is deleted (see the table below). `X2` closes as
   option (c): retire `--product aws-devops`.
3. **crackbox cut to its lightest** (`6/24`) — a static per-spawn forward
   proxy, ~250 lines, with `pkg/host` split out and `crackbox` surviving
   as the tool that composes them. **Demoted 2026-08-18**: the earlier
   framing was "get a stranger running it", but Anthropic ships
   sandboxing and network restriction inside Claude Code, so a standalone
   egress proxy competes with a free bundled feature. Do it for the
   internal reasons — 1,221 of ~1,700 lines serve an abandoned
   shared-proxy model, and its consumer-held unregister invariant is a real leak.
   The composition is the product, not the parts.
4. **The cockpit is SHIPPED** (`28`–`33`, completed 2026-08-30). `6/28`'s two
   contracts are real: `auth/dashauth.go` is the one operator gate every daemon
   dashboard applies (it replaced three drifted copies and added the CSRF check
   none had), and the per-daemon routes sit ahead of the `/dash/` catch-all with
   a test pinning that ordering. Eight daemons serve their own dashboard —
   onbod, timed, routd, runed, authd, proxyd, webd, ttsd — and davd deliberately
   serves none (dufs has no session list to render); the hub tile is its
   finished state. All ten channel adapters serve `/dash/<adapter>/`: seven Go
   ones from a single `chanlib/dash.go` mounted inside `NewAdapterMux`, whapd
   and twitd from their own `src/dash.ts`, since neither can import chanlib.
   dashd is the hub plus the cross-cutting pages, and **no dashboard reads
   another daemon's SQLite file** — all five retained pages read their owner's
   `/v1`. Writes stay direct per CLAUDE.md's split write-discipline, which is
   what keeps the operator on the audit row.
5. **Deferred by their own terms**: `19` waits for `5/13` shape 4; `12`
   (mcpfw) waits for a third-party-MCP consumer.

## The re-cut, applied 2026-08-27

Phase 5 closed on 2026-08-15 and this phase took `active` the same day, so
everything arriving after that landed here regardless of subject. The operator's
re-cut, recorded 2026-08-16 and refined the same day to the order the work
actually runs in: **6 splits arizuko into parts** — the tools, the components,
the contracts that let a part ship alone — and **7 puts the parts in the
cloud.** Splitting comes first because you cannot deploy a part you have not
separated.

Applied by moving the operator cockpit here, `7/1`·`7/2`·`7/3`·`7/11`·`7/12`·`7/41`
→ `28`–`33`. Nothing in phase 6 moved: the contracts (`7`), the strategy (`1`)
and the ship-in-parts map (`16`) are what let a part ship alone, so they were
already in the right phase. Phase 7 keeps `21` and means one thing.

**Why the destination numbers start at 28.** `1`–`27` have all been used in
this phase, several of them by more than one spec — `specs/6/1` has been five
different specs and `specs/6/2` six. Re-using one would turn every old pointer
to it into a confidently wrong pointer instead of a dangling one, which is the
failure `L43` measured.

## History

Absorbed 2026-08-27: the operator cockpit, `7/1`–`7/41` → `28`–`33` (above).

Dropped 2026-07-16: `3`+`4` (folded into `1`), `5`+`6`+`7` (folded into
`16`; the number `7` was later reused for the contracts spec),
`11` (messaging-gateway — premature), `13` (sandd — `runed` owns spawn).
Self-learning + skill-guard moved to `5/22`·`5/23`; the Hermes
integration moved to [`../5/35-hermes-integration.md`](../5/35-hermes-integration.md)
2026-08-02.

Deleted 2026-08-24 — the two superseded crackbox specs:

| was                         | why                                                                                                                                                                                                                                                                                                                                 |
| --------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `8-crackbox-standalone.md`  | the SHARED forward proxy with a registry, admin API, client library, transparent mode and SNI peek. `6/24` shipped 2026-08-18 and deleted all of it: one proxy per spawn, allowlist fixed at start. Its durable finding — the consumer-held unregister invariant was a real leak — is recorded in `6/24` §"It removes a real leak". |
| `10-crackbox-dns-filter.md` | the NXDOMAIN filter on UDP/53, keyed on the same registry as the proxy. `6/24` deleted `pkg/dns`: a single-tenant proxy already sees every destination in the CONNECT line, and a rule expressed twice can disagree with itself. `SECURITY.md:346` already states the supersession.                                                 |

Deleted 2026-08-02:

| was                             | why                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                              |
| ------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `2-target-matrix.md`            | competitive intel duplicating the Agent Research Hub, which it named as the real source of truth. Dated vendor tables rot; re-read the Hub instead.                                                                                                                                                                                                                                                                                                                                                                                                                                              |
| `17-agentic-distribution.md`    | self-disclaimed "recorded debate, NOT a build order" whose own codex critique folded it into `5/20` (since dissolved into `5/28`). The honest-removal delta landed in `5/28` (shipped 2026-07-29).                                                                                                                                                                                                                                                                                                                                                                                               |
| `17-egress-credential-proxy.md` | DECLINED then deleted 2026-08-20. Every tier refused: arizuko serves credentials through the MCP broker or not at all, and an opaque in-container client is out of scope **by decision**, not by coverage. Tier 0 (`credential_process`) had one named consumer, `X2`'s `aws-devops` product, which zero groups on any instance run. The two declines it recorded are now stated inline where they were cited: **interception/MITM is declined (2026-08-18)** and **vendor-signing transforms `aws_auth`/`hmac_sign` are declined (2026-08-15)**. `X2` closes as option (c), retire the product. |
| `slides.md`                     | a 10-slide pitch deck — presentation material, no buildable content.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                             |

`16`'s counterpart landscape was compressed to its durable finding (the
composition is the moat; never claim a commodity part is novel); the
dated 2026-07-15 vendor sweep table was cut for the same reason `2` was.

## Ties

`specs/5/A` (positioning) · `5/9` (`anteval` — the agent conformance
basis) · `5/34` (channel protocol) · `USELESS.md` (the honest gaps this
closes) · the Agent Research Hub · `5/22` self-learning (the loop turned
outward).
