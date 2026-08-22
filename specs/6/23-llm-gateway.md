---
status: draft
depends: [../5/14-credentials]
---

# specs/6/23 — the model gateway

Adopt an off-the-shelf LLM gateway so the agent container stops holding
the real model credential, and so spend is measured at the boundary
rather than reported by the thing spending it.

Two independent faults, one mechanism closes both.

## Fault 1 — the container holds the real credential

`store.EnvProfileKeys` (`store/secrets.go:115`) carries
`ANTHROPIC_API_KEY`, `CLAUDE_CODE_OAUTH_TOKEN`, `OPENAI_API_KEY` and
`CODEX_API_KEY`. Per `5/14` these are the one credential class that
reaches container env **by design** — every other capability credential
is broker-only (`X1`, closed 2026-08-09).

So the agent can read the real model credential out of its own
environment. There is no per-turn scoping and no revocation short of
rotating the credential for everyone who shares it.

## Fault 2 — cost is self-reported by the spender

`ipc.Result.Models` is "per-model token usage + **pre-computed cost**",
supplied by the agent in `submit_turn`, and it is **optional**:
"nil/empty when the agent doesn't report". `LogExternalCost`
(`ipc/ipc.go:223`) is likewise an MCP tool the agent calls to record its
own non-Anthropic spend.

`routd/dispatch.go:158` gates spawns on today's folder spend. That gate
enforces caps against numbers the metered party supplies. An agent that
under-reports, or reports nothing, writes no `cost_log` row and spends
against a gate that sees zero.

This is not a hypothetical drift: the field is documented as absent for
"ant versions before the cost-caps cutover", so a stale agent image
already produces unmetered turns.

### A second consumer, found 2026-08-22

`cost_log` is no longer only a spend record. `anteval` now waits for a case's
turn to record its cost before injecting the next case, because routd runs one
turn per folder and a case that outran its budget starved every case behind it
(BUGS `L68`). The eval's completion signal is therefore a number the spender
supplies — an agent that dies before `submit_turn` reports nothing, and the
suite falls back to a bounded wait plus a warning.

That is the same trust gap as Fault 2, reached from a different direction, and
it sharpens what the gateway buys: gateway-written accounting makes the signal
independent of the container's cooperation.

It also exposes a gap the gateway does NOT close. Turn COMPLETION is not cost:
`recordTurnResult` already calls `SetTurnState(turnID, "done")`, so routd knows
authoritatively when a turn ended, but no read surface exposes it — routd
mounts `/v1/cost` and `/v1/turns/{turn_id}/history`, and neither answers "is
this turn still running". anteval reaches for cost because turn state is not
readable, not because cost is the right signal. A `turn_state` read surface
(a resreg resource per `5/17`, like every other cold-tier entity) is the
orthogonal fix, and it is worth having whether or not the gateway ships.

Measured on the first clean smoke (krons 2026-08-22, 9/9): one case waited the
full 3-minute fallback and warned. Its injected message id never became a turn
id, because routd batches a folder's pending messages and the turn takes the
LAST id in the batch. Cost keyed by turn id therefore cannot answer "did MY
message finish" at all for a batched case — no gateway changes that, and a
`turn_state` resource keyed the same way would not either. The signal anteval
actually wants is "does this folder have a live turn", which routd already knows
and no surface reports.

## Why a gateway and not the egress proxy

**A gateway is configured, not intercepted.** The client is pointed at it
(`ANTHROPIC_BASE_URL`) and the gateway IS the endpoint. No TLS
termination, no CA inside the container, nothing spliced.

That is precisely why this works where the declined MITM tier did not, and
why declining that tier (2026-08-18) costs nothing here. Metering model
traffic never needed interception — it needed the client to be told where
to send it.

## The shape

- arizuko mints a **per-spawn virtual key** with a budget and a TTL, and
  emits it plus the base URL into the container.
- The gateway holds the real provider credential. The container never
  sees one.
- **Revocation is deleting the virtual key** — immediate, needing no
  cooperation from a running turn, and scoped to one spawn rather than
  everyone sharing the credential.
- **Metering is per-key by construction.** `cost_log` becomes
  authoritative because it is written from the gateway's accounting;
  `ipc.Result.Models` degrades from source-of-truth to corroboration, and
  a disagreement between them is a signal worth logging.

## Adopt Bifrost

`paradigmxyz`-style adoption, per `6/16`: take the mechanism, keep the
folder-shaped part.

**Bifrost** (Maxim AI) — Apache-2.0, single Go binary, self-hosted.
Purpose-built for Claude Code: `ANTHROPIC_BASE_URL` points the CLI at it
and `ANTHROPIC_AUTH_TOKEN` carries the virtual key, which Claude Code
sends as `Authorization: Bearer` automatically. Four-tier budget
hierarchy, per-request token counts, per-virtual-key cost attribution,
20+ providers with failover. Matches arizuko's stack (Go, one binary, no
Postgres).

LiteLLM is the richer alternative and the reference virtual-key model,
but it is Python + Postgres — heavy for the solo end of the spectrum
`CLAUDE.md` commits to.

**Subscription auth is a hard requirement, not a preference.**
`CLAUDE_CODE_OAUTH_TOKEN` already ships; subscription billing is
flat-rate where API keys bill per token, and at arizuko's turn volume the
API-key path is impractically costly. A gateway that speaks only API keys
is disqualified. Bifrost documents detecting an Anthropic MAX account
instead of API-key auth.

## Open question — where the OAuth credential lives

This decides whether the spec delivers security or only visibility, and
it must be answered from Bifrost's own documentation before any code:

- credential **at the gateway**, spawn gets a revocable virtual key →
  Fault 1 and Fault 2 both close;
- credential **forwarded from the client** → Fault 2 closes, Fault 1 does
  not, because the container still holds the real token.

LiteLLM's documented mechanism is the second
(`forward_client_headers_to_llm_api`). Bifrost's phrasing suggests the
first. Do not build on the suggestion.

## Coverage is bounded

A gateway can front a provider only where BOTH hooks exist: a base-URL
override and a token in env.

| Path                       | Base URL | Env token | Frontable |
| -------------------------- | -------- | --------- | --------- |
| Claude Code (OAuth or key) | yes      | yes       | yes       |
| Any API-key provider       | yes      | yes       | yes       |
| Codex on ChatGPT-OAuth     | no       | no        | **no**    |

`ant/skills/oracle/SKILL.md:39` documents the codex path as a host mount:
`HOST_CODEX_DIR` bind-mounted to `/home/node/.codex`, "no env vars
needed". Nothing to swap, nothing to redirect. Its meterable alternative
(`CODEX_API_KEY`) is the per-token billing this spec exists to avoid.

Worth stating plainly: that host mount is also arizuko's weakest
credential isolation — the container receives the operator's whole
ChatGPT session rather than a scoped token — and it is the one case a
gateway structurally cannot improve. Its own problem, not this spec's.

## Wiring

Emit `ANTHROPIC_BASE_URL` and the virtual key as `-e` in `buildArgs`.
**Anything not emitted there is silently dead inside the container** —
three prior instances (`ARIZUKO_MODEL`, `RUNED_RUN_TIMEOUT`,
`ARIZUKO_BACKEND`), the last of which killed the second-backend slot
without anyone noticing.

## Tier placement (`5/14`)

This moves the model credential OUT of the env-profile tier for the
container. The container receives a per-spawn virtual key — a scoped,
revocable capability, not a credential. The real provider credential
becomes infra (type 3): host `.env` or gateway config, never a table row
reaching a container. Env-profile rows remain for the BYOC case, but they
resolve INTO a gateway virtual key rather than into container env.

## Out of scope

Replacing crackbox's egress filtering (different concern, different
path), any form of interception (declined 2026-08-18), and the codex
host-mount path (above).

## Ties

`5/14` credential tiers · MITM declined 2026-08-18 (metering never needed
it) · `6/16` adopt-the-mechanism · BUGS `L49` (the proposal and its
verification notes) · `X2` (the same "container must not hold the raw
secret" argument, other half).
