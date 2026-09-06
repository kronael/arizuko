---
status: draft
depends: [23-llm-gateway, 37-store-model, ../5/16-mcp-rest-unification]
---

# specs/6/39 — the gateway owns spend

Decision: **where a model gateway is configured, LiteLLM is the ledger and
arizuko stores no spend.** The cap is enforced by the gateway, attribution is
carried on the request, and every operator surface reads the gateway's
numbers. `cost_log` and the spend arithmetic around it go.

A second ledger can only agree, lag, or lie. Today arizuko keeps one: routd
reads `/spend/logs` after a run returns and copies the result into `cost_log`,
so the reply lands before the cost does and the two are never guaranteed to
match. The fix is not a better copier. It is one number.

## What LiteLLM already holds (read 2026-09-06)

Measured against its own documentation, not inferred.

| Capability    | Where                                                                                        |
| ------------- | -------------------------------------------------------------------------------------------- |
| the spend row | `LiteLLM_SpendLogs`: api key, user, team, tags, end user, model, spend, tokens               |
| budgets       | key, internal user, team, team member, end user, tag, and per model                          |
| budget resets | `budget_duration`, e.g. `"1d"`; absent, a budget never resets                                |
| refusal       | 400/401 `ExceededTokenBudget: Current spend for token: … Max Budget for Token:`              |
| rate limits   | `tpm_limit`, `rpm_limit` per key, team and user; `model_tpm_limit` per model                 |
| model access  | `models` on a key or user                                                                    |
| reads         | `/spend/logs`, `/spend/keys`, `/spend/users`, `/user/daily/activity`, `/global/spend/report` |

(docs.litellm.ai `proxy/cost_tracking`, `proxy/users`.)

Every one of those is a thing arizuko either does by hand today or does not do
at all. Rate limits and per-model access it does not do at all.

## The mapping

Three arizuko coordinates, three LiteLLM ones, set at mint time:

- **folder → team.** The folder's `cost_cap_cents_per_day` becomes the team's
  `max_budget` with `budget_duration: "1d"`. A folder's cap is then enforced
  by the thing that meters it, not by a sum arizuko keeps.
- **person → end user.** The turn's caller sub rides the request, so
  per-person attribution and a per-person cap are the gateway's `end_user`.
- **turn → key.** `6/23` already mints one key per turn with
  `metadata{folder, turn_id}`; it gains the caller sub.

The mapping is the only thing arizuko stores: which team id a folder is. One
row, written when the folder is created.

## What "synchronous" means here, honestly

LiteLLM writes the spend row on a batch timer — `proxy_batch_write_at`, which
the gateway package sets to 1 second. So no design makes a spend row readable
the instant a call returns.

What this decision buys is not zero latency. It is that **there is one number**.
A dashboard reading `/spend/logs` shows what the gateway metered, late by the
flush and no more. A cap is checked by the gateway on the request itself, which
is exactly when it matters. Nothing in arizuko can disagree with the meter,
because nothing in arizuko holds a copy.

## What goes

- `cost_log`, its migration, and every writer and reader — 18 files touch it.
- `routd/db.go` `SpendTodayFolder` and `SpendTodayUser`.
- `routd/budget.go`'s spend arithmetic. The pre-spawn gate stays as a shape
  but asks the gateway, and its refusal message quotes the gateway's.
- `routd/gateway.go` `spend()` and the poll that follows a run — routd stops
  reading spend to write it down.
- `dashd/usage_page.go`'s cost queries, which become a proxied read.

## What arizuko keeps, and why

- **The cap decision.** What a folder may spend is an operator policy in
  arizuko's `groups` row. Only its ENFORCEMENT moves.
- **The turn record.** `turns` keeps the turn, its folder, its caller and its
  outcome. That is arizuko's data; what it cost is not.
- **The agent's self-report, as a log line only.** With no gateway it is the
  only number there is, and with one it is the signature of a bypassed
  gateway when the two disagree.

## The instance with no gateway

`MODEL_GATEWAY_URL` unset is a supported deployment, and it has no meter. Then
there is no spend surface at all: the dashboard says so rather than showing a
number the agent chose. That is the honest reading of a harness the operator
did not put a meter in front of, and it is the reason a cap needs a gateway.

## Order

1. The mapping: a folder's cap becomes a LiteLLM team on creation, and the
   mint sends the caller sub as `end_user`. Additive, nothing reads it yet.
2. The pre-spawn gate asks the gateway instead of `cost_log`, and surfaces the
   gateway's refusal to the channel.
3. The dashboard reads `/spend/logs` and `/user/daily/activity`.
4. `cost_log` and its arithmetic are deleted, with the migration that drops
   the table last.

Steps 1 to 3 are additive and revert with `git revert`. Step 4 is the point of
no return for historical rows, and it is the only one that needs a decision
about what happens to the spend already recorded.

## Ties

`6/23` (the gateway and the per-turn key) · `6/37` (one opener; `cost_log` is
one of the tables the Postgres port would otherwise carry) · BUGS `L233` (the
async gap this removes) · `L49`.
