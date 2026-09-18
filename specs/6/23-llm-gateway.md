---
status: shipped
depends: [../5/14-credentials]
---

# specs/6/23 — the model gateway

An off-the-shelf LLM gateway holds the model credential so the agent
container never does, and meters what a turn spends at the boundary its
tokens cross instead of believing the thing that spent them.

Two independent faults, one mechanism closes both.

## Fault 1 — the container holds the real credential

`store.EnvProfileKeys` (`store/secrets.go:116`) carries
`ANTHROPIC_API_KEY`, `CLAUDE_CODE_OAUTH_TOKEN`, `OPENAI_API_KEY` and
`CODEX_API_KEY`. Per `5/14` these are the one credential class that
reaches container env **by design** — every other capability credential
is broker-only (`X1`, closed 2026-08-09).

An agent that reads its own environment therefore reads the real model
credential. Nothing scopes it to the turn and nothing revokes it short of
rotating the credential for everyone who shares it.

## Fault 2 — cost is self-reported by the spender

`ipc.Result.Models` is "per-model token usage + **pre-computed cost**",
supplied by the agent in `submit_turn`, and it is **optional**:
"nil/empty when the agent doesn't report". `LogExternalCost`
(`ipc/ipc.go:225`) is likewise an MCP tool the agent calls to record its
own non-Anthropic spend.

`routd/dispatch.go:261` gates spawns on today's folder spend. A gate fed
by the metered party enforces nothing: an agent that under-reports, or
reports nothing, writes no `cost_log` row and spends against a view that
sees zero. The field is documented as absent for "ant versions before the
cost-caps cutover", so a stale agent image produces unmetered turns with
no attacker at all.

### A second consumer

`cost_log` is also `anteval`'s completion signal: it waits for a case's turn
to record its cost before injecting the next case, because routd runs one
turn per folder and a case that outran its budget starved every case behind it
(BUGS `L68`). A signal the spender supplies is a signal an agent that dies
before `submit_turn` never sends, and the suite falls back to a bounded wait
plus a warning.

That is the same trust gap as Fault 2 reached from another direction, and it
sharpens what the gateway buys: gateway-written accounting is independent of
the container's cooperation.

## Why a gateway and not the egress proxy

**A gateway is configured, not intercepted.** The client is pointed at it
(`ANTHROPIC_BASE_URL`) and the gateway IS the endpoint. No TLS
termination, no CA inside the container, nothing spliced. Metering model
traffic never needs interception — it needs the client to be told where to
send it.

## The shape

- routd mints a **virtual key per turn** carrying a budget and a TTL, and
  hands it to runed with the run; runed emits it plus the base URL into
  the container.
- The gateway holds the real provider credential. The container never
  sees one, and `spawnSecrets` (`container/runner.go:787`) withholds the
  model keys from both the host anchor and the trigger user's own
  env-profile row whenever a gateway is configured.
- **Revocation is deleting the virtual key** — immediate, needing no
  cooperation from a running turn, and scoped to one turn rather than
  everyone sharing the credential. The TTL is the backstop for a routd
  that dies before it deletes.
- **Metering is per-key by construction.** `cost_log` is written from the
  gateway's own accounting for that key; `ipc.Result.Models` is
  corroboration in the log, never the row.

## Adopt LiteLLM

`paradigmxyz`-style adoption, per `6/16`: take the mechanism, keep the
folder-shaped part.

**LiteLLM** (`ghcr.io/berriai/litellm:main-latest`) is the gateway
`template/services/litellm.yml` ships. It is the reference virtual-key
model: `POST /key/generate` takes a `duration`, a `max_budget` and a
`key_alias`, `GET /spend/logs?api_key=<sha256 of the key>` returns that
key's per-request usage, and `POST /key/delete` revokes it.

**Subscription auth is a hard requirement, not a preference.**
`CLAUDE_CODE_OAUTH_TOKEN` already ships; subscription billing is
flat-rate where API keys bill per token, and at arizuko's turn volume the
API-key path is impractically costly. A gateway that speaks only API keys
is disqualified, and that requirement is what settles the vendor: LiteLLM
recognises an `sk-ant-oat` value and switches to `Authorization: Bearer`
plus the OAuth beta header on its own, so the subscription credential
reaches Anthropic intact. **Bifrost** (Maxim AI, Apache-2.0, one Go
binary) matches arizuko's stack far better and is rejected on this one
point: it writes the upstream credential as `x-api-key` and has no path
for an OAuth bearer, so a subscription token behind it buys nothing.

## The gateway carries its own database

Virtual keys, per-key budgets and `/spend/logs` are Postgres-only in
LiteLLM: with no `DATABASE_URL` the proxy forwards requests and records
nothing per key. So the package ships `litellm_db` beside the gateway —
a Postgres named in the same fragment, healthy before the gateway boots,
wired by `DATABASE_URL`.

This is the gateway's bookkeeping and not arizuko's state, which decides
two things. It lives in a named volume rather than under the instance data
dir that backup and restore own, and it changes nothing about arizuko's own
stores: `routd.db` and its siblings stay SQLite (`6/37`).

`general_settings: proxy_batch_write_at: 1` in the fragment is
load-bearing. LiteLLM writes usage rows on a batch timer whose default is
10 seconds plus up to 5 of jitter, and routd waits for that flush at the
turn boundary before it writes `cost_log`.

## Wiring — who holds which half

`MODEL_GATEWAY_URL` and `MODEL_GATEWAY_KEY` are ONE declaration in the
instance `.env`, minted by `packages add` from
`template/services/litellm.yaml`. They are read in three places, each
needing a different half at a different time:

| Reader | Reads                                    | Why                                                                                    |
| ------ | ---------------------------------------- | -------------------------------------------------------------------------------------- |
| routd  | URL + master key                         | mints each turn's key, reads its spend, deletes it                                     |
| runed  | URL                                      | re-emits it as container env; allows the gateway host through the spawn's egress proxy |
| ant    | URL + the minted key, from container env | translates the pair into each harness's own shape                                      |

The master key never reaches runed, and no container ever holds anything
but the key minted for its own turn. routd is the minter because the two
numbers the key carries are routd's rows — the folder's cap and today's
spend — and runed has no store at all (`5/E`). The same daemon owns
`cost_log`, so the measurement comes back where its only writer lives.

The lifecycle is one bracket around one dispatch (`routd/dispatch.go`):
`budgetGate` yields the refusal and the remaining cents, `mint` turns the
cents into `max_budget` and the run ceiling into `duration`, and
`dispatchRun` carries the key in `RunRequest.ModelGatewayKey` the way it
already carries routd-resolved `Secrets`. One `defer` closes the bracket —
`recordGatewayCost` (`routd/dispatch.go:558`) reads what the key was
charged, then `revoke` deletes it — so every way out of the turn books it,
including a run whose reply never came back and which may still be
spending. Two dispatches skip the read because they have nothing to read:
a Busy refusal admitted no run, and a steer spent on the key of the turn
that owns the container.

`putTurnCost` (`routd/turns.go:196`) is the ONE writer of a turn's
`cost_log` rows. Exactly one source reaches it per turn — the gateway's
measurement at the turn boundary, or the agent's own report where no
gateway is configured — so the `(folder, turn_id, model)` primary key
never has to arbitrate between two numbers for the same call.

**Anything not emitted in `buildArgs` is silently dead inside the
container** — three prior instances (`ARIZUKO_MODEL`,
`RUNED_RUN_TIMEOUT`, `ARIZUKO_BACKEND`), the last of which killed the
second-backend slot without anyone noticing.

Unset `MODEL_GATEWAY_URL` means the container keeps the credential and
talks to the vendor directly. That is the default, it is what every
instance runs, and it is fine for a small deployment where the operator
and the agent's users are the same people.

## Failure is loud in both directions

- A gateway that will not mint **fails the turn** as a dependency outage:
  the batch re-feeds, the folder's breaker streak is untouched, and no
  container starts. Spawning anyway would put a container on the network
  holding neither a virtual key nor the model credential `spawnSecrets`
  withheld from it. `container.Run` refuses the same shape at its own
  boundary, so a routd/runed version skew reports itself instead of
  producing a 401 storm the operator reads as a harness fault.
- A spend read that **fails** logs at ERROR and delivers a notice to the
  chat. The reply already went out; what is broken is the accounting, and
  a cap that counts nothing is the fault this document exists to close.
- A spend read that **succeeds and finds nothing** is a turn that reached
  no model. It logs at WARN and writes no row. The two cases must never
  look alike, which is why the gateway read returns an error for a failed
  request and an empty map for an empty answer, never a zero.

## Coverage is bounded — by the vendor, not by the redirect

Every harness has a redirect hook. Measured 2026-09-04 against a local
sink and a local forwarding gateway, all three reached it:

| Harness | Redirect mechanism                                          |
| ------- | ----------------------------------------------------------- |
| claude  | `ANTHROPIC_BASE_URL` + `ANTHROPIC_AUTH_TOKEN` (env)         |
| codex   | `-c model_providers.<id>.base_url` + `requires_openai_auth` |
| pi      | `~/.pi/agent/models.json` provider with `baseUrl`           |

`requires_openai_auth = true` is what makes codex send its ChatGPT
subscription bearer to an arbitrary `base_url` instead of demanding an
`env_key` API key. Its upstream is `chatgpt.com/backend-api/codex`, and
`wire_api` MUST be `responses` — codex-cli 0.153.2 rejects `chat`
outright. The ChatGPT-OAuth path is frontable: the container holds a
placeholder `auth.json` while the gateway injects the real token
(verified end to end, upstream 200).

The real boundary is the VENDOR'S client check, not the redirect. With a
subscription credential, only a first-party client draws on plan limits:

| Client     | Through the gateway on plan credentials                                           |
| ---------- | --------------------------------------------------------------------------------- |
| claude CLI | 200, `anthropic-ratelimit-unified-5h/7d` headers                                  |
| codex      | 200 on `/responses`, plan quota consumed                                          |
| pi         | **400** — "Third-party apps now draw from your extra usage, not your plan limits" |

Same gateway, same injected credential, same upstream: the difference is
which client is asking. So pi needs extra-usage credits or an API key,
and no gateway change fixes that. Making pi draw plan quota would require
forging another client's identity, which arizuko does not do.

With an API key rather than a subscription token, all three work
uniformly and the client check does not apply.

## Tier placement (`5/14`)

The model credential is OUT of the env-profile tier for the container. The
container receives a per-turn virtual key — a scoped, revocable
capability, not a credential. The real provider credential is infra
(type 3): host `.env` or gateway config, never a table row reaching a
container. Env-profile rows remain for the BYOC case, but they resolve
INTO a gateway virtual key rather than into container env.

## Operator note: which credential the gateway holds

The gateway is credential-agnostic; the operator's config decides. The
two choices differ in more than billing, so record the trade rather than
defaulting silently.

An **API key** in the gateway is the shape Anthropic documents for
provisioning "in a development environment, secrets manager, or machine
image for use by the customer's own authorized users"
(code.claude.com/docs/en/legal-and-compliance). Every client works
uniformly.

A **subscription token** in the gateway keeps plan billing for
first-party clients, and the same page states that developers "may not
collect, store, or intermediate Claude.ai credentials or session tokens".
An operator pointing this at their own account should read it and decide;
arizuko does not make the choice for them, and ships no mechanism that
disguises which client is calling.

## What this does not close

- **A steered turn is metered on the run it steered into.** A steer
  writes its batch into a container another turn already owns, so its
  model calls are charged to that turn's key. The steering turn mints a
  key, spends nothing on it, and deletes it. Attribution is per-run, not
  per-batch.
- **`cost_log` holds whole cents.** The gateway reports dollars, and
  rounding happens once over each model's total for the turn rather than
  per request, so a turn of sub-cent calls records what it cost. A turn
  whose whole spend is under half a cent still records zero.
- **The agent's report is corroborated by a human, not by code.** Both
  numbers reach the log with the same `turn_id`; nothing compares them
  automatically, so a harness that spends where the gateway cannot see it
  is visible in the log and raises no alert.
- **A folder with no cap hands its key no budget.** The TTL is then the
  only bound on the key, because a per-turn ceiling invented here would be
  a policy the operator never set.

## Ties

`5/14` credential tiers · `6/16` adopt-the-mechanism · `6/37`
(Postgres as substrate for the adopted tools, SQLite for arizuko's own
stores) · BUGS `L49` · `X2` (the same "container must not hold the raw
secret" argument, other half).
