---
status: draft
depends: [23-llm-gateway, 37-store-model, ../5/16-mcp-rest-unification]
---

# specs/6/39 — the gateway owns spend

Decision: **where a model gateway is configured, LiteLLM is the ledger and
arizuko stores no spend.** The cap is enforced by the gateway, attribution is
carried on the key, and every operator surface reads the gateway's numbers.
`cost_log` and the spend arithmetic around it go.

A second ledger can only agree, lag, or lie. Today arizuko keeps one: routd
reads `/spend/logs` after a run returns and copies the result into `cost_log`,
so the reply lands before the cost does and the two are never guaranteed to
match. The fix is not a better copier. It is one number.

## What LiteLLM already holds

Verified 2026-09-06 against a running `ghcr.io/berriai/litellm:main-latest`:
routes extracted from the image's own source, request models read by pydantic
introspection, every call below driven against the booted proxy.

| Capability    | Where                                                                                    |
| ------------- | ---------------------------------------------------------------------------------------- |
| the spend row | `LiteLLM_SpendLogs`: api key (sha256), user, team, tags, end user, model, spend, tokens  |
| budgets       | key, internal user, team, team member, end user, tag, and per model                      |
| budget resets | `budget_duration`, e.g. `"1d"`; absent, a budget never resets                            |
| refusal       | 429 `budget_exceeded`: `Budget has been exceeded! Team=… Current cost: …, Max budget: …` |
| rate limits   | `tpm_limit`, `rpm_limit` per key, team and user; `model_tpm_limit` per model             |
| model access  | `models` on a key or user                                                                |
| folder reads  | `/team/info` (current window), `/team/daily/activity` (per team per day)                 |

Every one of those is a thing arizuko either does by hand today or does not do
at all. Rate limits and per-model access it does not do at all.

## The mapping

Three arizuko coordinates, three LiteLLM ones, set at mint time:

- **folder → team, by identity.** A LiteLLM `team_id` takes a folder path
  verbatim (`corp/eng/sre` round-trips through `/team/new` and `/team/info`),
  so the mapping is the identity function and arizuko stores nothing for it.
  The folder's `cost_cap_cents_per_day` becomes the team's `max_budget` with
  `budget_duration: "1d"`, so a folder's cap is enforced by the thing that
  meters it.
- **person → internal user.** `/key/generate` takes `user_id`, and it is routd
  that sets it, so the caller a turn is attributed to is not a field the
  container can rewrite. It lands on the spend row's `user` column, and the
  person's cap is that internal user's `max_budget`.
- **turn → key.** `6/23` already mints one key per turn with
  `metadata{folder, turn_id}`; it gains `team_id` and `user_id`.

### Why the folder is the team id, and not a stored mapping

A stored `folder → team_id` row is a second declaration of a fact the `groups`
row already carries, and it is the shape that drifts: a wiped gateway leaves the
row pointing at a team that no longer exists. Identity cannot drift, needs no
migration and no backfill, and covers every folder that already exists. The
team's `team_alias` carries the folder too, so the gateway's own UI reads in
arizuko's names.

### Why the person axis is `user_id` and not `end_user`

`/key/generate` has no `end_user` field. LiteLLM learns an end user from the
request body's `user` field or from an `x-litellm-end-user-id` header — both
written inside the container, which would let the spender choose whose budget it
spends. `user_id` is set by routd at mint time on a credential the container only
holds, so the attribution is the harness's and the cap it selects is the one the
operator set.

## The team must exist before the turn runs

A key minted with a `team_id` that has no team row is accepted, and then every
model call on it fails `404 Team doesn't exist in db`. So reconciling the team is
not an optimisation that can be skipped for an uncapped folder — it is a
precondition of the turn. The gate does it, which is why the gate runs even when
`COST_CAPS_ENABLED` is off.

One `GET /team/info` per turn does all three jobs: it creates the team when the
folder has none, it converges the team's `max_budget` on the cap the operator set,
and it reads the spend the refusal decides on. One read, one number, no cache to
invalidate.

## What "synchronous" means here, honestly

LiteLLM writes the spend row on a batch timer — `proxy_batch_write_at`, which
the gateway package sets to 1 second. So no design makes a spend row readable
the instant a call returns.

What this decision buys is not zero latency. It is that **there is one number**.
A dashboard reading the gateway shows what the gateway metered, late by the
flush and no more. A cap is checked by the gateway on the request itself, which
is exactly when it matters. Nothing in arizuko can disagree with the meter,
because nothing in arizuko holds a copy.

## What goes

- `cost_log`'s writers and readers — `PutCost`, `CostByTurn`, `CostSummary`
  over the table, `LogExternalCost`, the `log_external_cost` MCP tool, and
  `POST /v1/cost`. A call the agent makes outside the gateway is a bypassed
  gateway, and the spec's answer to that is a log line, not a row.
- `routd/db.go` `SpendTodayFolder` and `SpendTodayUser`.
- `routd/budget.go`'s spend arithmetic. The pre-spawn gate stays as a shape
  but asks the gateway, and its refusal quotes the gateway's numbers.
- `routd/gateway.go` `spend()`, `waitForSpend()` and the poll that follows a
  run — routd stops reading spend to write it down. The per-turn key stops
  carrying a `max_budget`, because the team is what enforces the folder's cap.
- `dashd/usage_page.go`'s cost queries, which become a proxied read.

## What arizuko keeps, and why

- **The cap decision.** What a folder may spend is an operator policy in
  arizuko's `groups` row, and what a person may spend is theirs in
  `user_profiles`. Only ENFORCEMENT moves; the gate pushes both to the gateway.
- **The turn record, including which key metered it.** `turn_context` keeps the
  turn, its folder, its caller, its outcome and the sha256 of the key minted for
  it. Which credential paid is the turn's own identity; what it bought is not.
  That one pointer is what keeps `GET /v1/cost?turn_id=` answerable without a
  copy — it resolves the turn to its key and asks the gateway. `anteval` reads
  it as both the settle barrier between cases and the `max_tokens` budget, so
  the endpoint stays; only its source moves.
- **The agent's self-report, as a log line only.** With no gateway it is the
  only number there is, and with one it is the signature of a bypassed
  gateway when the two disagree.

## The spend already recorded

`cost_log` holds real history — 8,848 rows and about $4,624 across three live
instances, back to 2026-05-14, from months the gateway did not meter. LiteLLM's
own Postgres has none of it, so a drop destroys the only copy.

Step 4 therefore **renames** the table to `cost_log_archive` rather than
dropping it. The rows survive byte for byte with no export to misplace, and the
rename is what makes the cutover safe rather than merely careful: any writer left
behind fails loud on a missing table instead of quietly starting a second ledger
again. Nothing reads the archive and nothing writes it; an operator reads it with
`sqlite3` when they want the history, and the dashboard does not show it, because
a second number on the operator's screen is the thing this spec removes.

## The instance with no gateway

`MODEL_GATEWAY_URL` unset is a supported deployment, and it has no meter. Then
there is no spend surface at all: the dashboard says so rather than showing a
number the agent chose. A cap set on such an instance is enforced by nothing, so
the gate says so in the log rather than pretending — that is the honest reading
of a harness the operator did not put a meter in front of, and it is the reason a
cap needs a gateway.

## Order

1. The mapping: the folder is the team, the mint sends `team_id` and `user_id`.
2. The pre-spawn gate reconciles the team and asks the gateway, and surfaces the
   gateway's refusal to the channel.
3. The operator surfaces read the gateway.
4. `cost_log` and its arithmetic go, and the table becomes `cost_log_archive`.

Steps 1 to 3 are additive and revert with `git revert`. Step 4 is the one that
moves stored rows, and the rename is what keeps it reversible.

## Ties

`6/23` (the gateway and the per-turn key) · `6/37` (one opener; `cost_log` is
one of the tables the Postgres port would otherwise carry) · BUGS `L233` (the
async gap this removes) · `L49`.
