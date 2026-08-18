---
status: active
---

# specs/6 — adoption & interop

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

## Specs

| Spec                                                               | Status  | Covers                                                                                                                                                                                                     |
| ------------------------------------------------------------------ | ------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| [7-environment-contracts.md](7-environment-contracts.md)           | draft   | **the spine**: the agent contract (identity, home, envelope, idempotent delivery, unfrozen lifecycle seam) + the component contract; conformance = `anteval` per backend / a live probe                    |
| [1-adoption-interop.md](1-adoption-interop.md)                     | draft   | adoption strategy: publish-the-contract thesis (Lambda analogy), the wrap demoted to on-ramp, the agentic reimplementation loop, the seven services, demand-class mode                                     |
| [16-daemon-standalone-matrix.md](16-daemon-standalone-matrix.md)   | draft   | the four-layer model (surfaces / core / substrate / agent — `ant` is the first layer-4 occupant) + two standalone axes + the matrices; `servekit` retired; crackbox's ship gate cleared                    |
| [8-crackbox-standalone.md](8-crackbox-standalone.md)               | shipped | crackbox — forward proxy with per-source allowlists; arizuko's per-folder egress consumer (was `12/9`)                                                                                                     |
| [9-crackbox-sandboxing.md](9-crackbox-sandboxing.md)               | shipped | crackbox `pkg/host/` KVM/qemu sandbox library — **shipped but unwired**: no importer outside `crackbox/`; `runed` still spawns Docker (was `12/12`)                                                        |
| [10-crackbox-dns-filter.md](10-crackbox-dns-filter.md)             | shipped | DNS NXDOMAIN filter on UDP/53; reuses `Registry`+`match.Host` (was `12/15`)                                                                                                                                |
| [12-mcp-firewall.md](12-mcp-firewall.md)                           | draft   | `mcpfw` — transparent MCP proxy for THIRD-PARTY MCP servers; deny-wins tool-call filter (was `12/17`)                                                                                                      |
| [17-egress-credential-proxy.md](17-egress-credential-proxy.md)     | draft   | the opaque-client credential hole (`8/Z`): `credential_process` closes `X2`, signing rides a host-side request tool. MITM + header injection DECLINED 2026-08-18 — the allowlist never needed interception |
| [18-durable-workflows-decline.md](18-durable-workflows-decline.md) | shipped | costed DECLINE of a durable-execution engine — `5/A` decision 5 stands; reopen trigger + the four-table shape to copy if it fires                                                                          |
| [19-unified-tool-declaration.md](19-unified-tool-declaration.md)   | draft   | collapse `[[ext]]`+`[[mcp_connector]]` into one `[[connector]]` block + the ordered-source shadow rule; timed to `5/13` shape 4                                                                            |
| [20-sandbox-lifecycle-decline.md](20-sandbox-lifecycle-decline.md) | shipped | costed DECLINE of the warm-pool sandbox lifecycle vs per-turn ephemeral containers; the mount-boundary blocker; `can_open_io`+reconcile survive for `6/9` KVM                                              |
| [22-proxyd-cut.md](22-proxyd-cut.md)                               | draft   | cut proxyd to its irreplaceable half — move the misfiled token handlers out (step 1, standalone), routes as rows (step 2, `L45`), forward-auth split (step 3, unsigned). Does NOT remove proxyd            |
| [23-llm-gateway.md](23-llm-gateway.md)                             | draft   | adopt Bifrost so the container holds a per-spawn virtual key, not the real model credential; cost measured at the boundary instead of self-reported by the agent                                           |
| [24-crackbox-lightest.md](24-crackbox-lightest.md)                 | draft   | cut crackbox to one static per-spawn forward proxy (~250-300 lines): no registry, no admin API, no transparent mode, no SNI peek. Shrink, not adopt — at that size a dependency costs more than it saves   |

`7` owns the contracts; `1` owns the strategy; `16` owns the map (layers,
axes, matrices). `8`–`10` (crackbox family) and `12` (`mcpfw`) are the
concrete component specs from the former phase 12. `17`–`20` are the
**Centaur peel** (`paradigmxyz/centaur@20f3021`, Apache-2.0, read
2026-08-10): `18` and `20` shipped as decision records (decline, costed,
with reopen triggers); `17` was narrowed twice on 2026-08-15 (signing →
request tool; vendor-signing transforms declined); `19` is timed, not
standalone.

## The plan from here

The phase is `active` (2026-08-15). What ships, in the order the specs
themselves argue:

1. **The `6/7` contracts made real** — the component probe **shipped
   2026-08-17** (`arizuko probe <base-url>`); routd and proxyd pass 5/5
   live, dashd 3/3. What remains is the `anteval` gate run per backend,
   with `pi` as the second agent-slot occupant.
2. **`6/17` tier 0** — the `credential_process` helper closes BUGS `X2`
   with no new trust surface; the request tool carries signing.
3. **crackbox cut to its lightest** (`6/24`) — a static per-spawn forward
   proxy, ~250 lines, with `pkg/host` split out and `crackbox` surviving
   as the tool that composes them. **Demoted 2026-08-18**: the earlier
   framing was "get a stranger running it", but Anthropic ships
   sandboxing and network restriction inside Claude Code, so a standalone
   egress proxy competes with a free bundled feature. Do it for the
   internal reasons — 1,221 of ~1,700 lines serve an abandoned
   shared-proxy model, and `6/8`'s unregister invariant is a real leak.
   The composition is the product, not the parts.
4. **Deferred by their own terms**: `19` waits for `5/13` shape 4; `12`
   (mcpfw) waits for a third-party-MCP consumer; `17` tier 1 (MITM)
   waits for a consumer tier 0 cannot serve.

## Open: this phase holds two different kinds of work

Recorded 2026-08-16, **not yet applied** — it moves files and needs a
deliberate pass.

Phase 5 closed on 2026-08-15 and this phase took `active` the same day, so
everything arriving after that landed here regardless of subject. Only four
specs argue adoption — `1` (strategy), `7` (the contracts), `16` (ship-in-
parts), `8` (crackbox as the first shipped part). The rest are platform work,
and their own frontmatter says so: `17` closes `8/Z`, a gap **phase 8** named;
`19` depends on `5/13`+`5/14` and is timed to `5/13` shape 4; `12` has no
consumer; `18` and `20` are runtime decisions (`21` left for `7/21`); `9` and `10`
are crackbox internals.

The operator's re-cut: **6 = finish and harden the platform** (absorbing the
phase-7 cockpit), **7 = adoption** (contracts, conformance, ship-in-parts,
cloud). The work order is the point; the digits are the cost. Spec IDs appear
222 times across 64 files including `BUGS.md`, `CHANGELOG.md` and diary
history, and `6/1`/`7/1` collide, so the renumber is its own change with its
own verification — never a side effect of another edit.

## History

Dropped 2026-07-16: `3`+`4` (folded into `1`), `5`+`6`+`7` (folded into
`16`; the number `7` was later reused for the contracts spec),
`11` (messaging-gateway — premature), `13` (sandd — `runed` owns spawn).
Self-learning + skill-guard moved to `5/22`·`5/23`; the Hermes
integration moved to [`../5/35-hermes-integration.md`](../5/35-hermes-integration.md)
2026-08-02.

Deleted 2026-08-02:

| was                          | why                                                                                                                                                                                                |
| ---------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `2-target-matrix.md`         | competitive intel duplicating the Agent Research Hub, which it named as the real source of truth. Dated vendor tables rot; re-read the Hub instead.                                                |
| `17-agentic-distribution.md` | self-disclaimed "recorded debate, NOT a build order" whose own codex critique folded it into `5/20` (since dissolved into `5/28`). The honest-removal delta landed in `5/28` (shipped 2026-07-29). |
| `slides.md`                  | a 10-slide pitch deck — presentation material, no buildable content.                                                                                                                               |

`16`'s counterpart landscape was compressed to its durable finding (the
composition is the moat; never claim a commodity part is novel); the
dated 2026-07-15 vendor sweep table was cut for the same reason `2` was.

## Ties

`specs/5/A` (positioning) · `5/9` (`anteval` — the agent conformance
basis) · `5/34` (channel protocol) · `USELESS.md` (the honest gaps this
closes) · the Agent Research Hub · `5/22` self-learning (the loop turned
outward).
