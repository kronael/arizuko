---
status: spec
---

# Cost caps + ephemeral budget nudges

Per-channel and per-user token / dollar budgets, with mid-session
ephemeral nudges when the agent approaches a cap. Public-channel
deployments today have no money governor — a single chatty thread
can rack up real bill. Adopted from muaddib's `src/session-factory.ts`
(lines 282-322), which has proven this UX on a live IRC deployment.

## Why this isn't "just set a low max_tokens"

Three failure modes a naive cap would hit:

1. **Hard kill mid-turn.** Agent in the middle of useful work hits
   the cap, response truncates, user sees a broken sentence. No
   recovery path.
2. **Operator surprise.** A team blasts through their monthly
   budget over a weekend; operator notices when the Anthropic
   invoice arrives. Damage already done.
3. **No per-user accountability.** One user hammers the bot for
   hours; the channel as a whole gets throttled because there's no
   way to scope the cost.

The design has two budgets (per-channel daily, per-user daily) +
two soft warnings (50%, 80%) + a hard stop at 100%, all with
**ephemeral nudges** that get appended to the per-turn envelope so
the agent self-throttles before the operator has to.

## Mechanism

### Cost tracking

`store/cost_log.go` (new) writes one row per LLM call:

```sql
CREATE TABLE cost_log (
  at         TEXT NOT NULL,
  folder     TEXT NOT NULL,
  user_sub   TEXT NOT NULL,            -- '' for channel-scoped turns
  model      TEXT NOT NULL,
  input_tok  INTEGER NOT NULL,
  output_tok INTEGER NOT NULL,
  cents      INTEGER NOT NULL          -- precomputed: model price × tok
);
CREATE INDEX idx_cost_log_folder_at ON cost_log(folder, at);
CREATE INDEX idx_cost_log_user_at   ON cost_log(user_sub, at);
```

Anthropic's API response carries `usage.input_tokens` and
`usage.output_tokens` per call; the ant container forwards these
to gated via the MCP `submit_turn` envelope (existing path; just
adds token/cost fields). Gated writes one cost_log row per call.

### Budgets

Per-channel and per-user, in `chats` and `auth_users`:

```sql
ALTER TABLE chats      ADD COLUMN cost_cap_cents_per_day INTEGER NOT NULL DEFAULT 0;
ALTER TABLE auth_users ADD COLUMN cost_cap_cents_per_day INTEGER NOT NULL DEFAULT 0;
```

`0` means "no cap" (default; existing channels/users unaffected).
Operator sets explicit caps via dashd or CLI.

### Mid-session nudges

At the start of each turn, gateway computes `spent_today =
SUM(cents) FROM cost_log WHERE folder = X AND at > today_start`
(same for user). If `spent_today / cap >= 0.5`, append an ephemeral
block to the per-turn envelope:

```xml
<budget_notice level="50">
This channel has spent 53% of today's budget ($1.06 of $2.00).
Consider keeping responses concise.
</budget_notice>
```

At 80%:

```xml
<budget_notice level="80">
This channel has spent 82% of today's budget ($1.64 of $2.00).
Stop unless the user explicitly continues; warn them on first
output.
</budget_notice>
```

At 100% — hard stop. Gateway refuses to spawn the agent; sends a
short ephemeral reply directly to the channel (without an LLM call):

> Budget reached for today ($2.00). Resumes 00:00 UTC. Operator
> can raise the cap at `/dash/groups/<folder>/`.

The user-scope cap behaves identically against per-user spend.
Both caps must pass for a turn to proceed; the lower bound wins.

### Ephemeral, not persisted

The `<budget_notice>` block is appended to the per-turn envelope
only — it isn't stored as part of the conversation history. Next
turn recomputes from scratch. The agent doesn't accumulate
budget-notice text across the session.

## Configuration

Per-instance defaults in `.env`:

```
COST_CAPS_ENABLED=true                  # global kill switch
COST_CAPS_WARN_THRESHOLDS=0.5,0.8       # nudge levels
COST_DAILY_RESET=00:00 UTC              # window reset
COST_MODEL_PRICES_PATH=/etc/arizuko/prices.toml   # see below
```

Model price table (`prices.toml`, operator-editable):

```toml
[claude-opus-4-7]
input_per_million  = 1500   # cents
output_per_million = 7500

[claude-sonnet-4-6]
input_per_million  = 300
output_per_million = 1500
```

Per-channel + per-user overrides land in the schema columns above;
operators edit via dashd (Phase 1: read-only listing; Phase 2: a
form to set caps).

## Operator dashboard

`/dash/groups/<folder>/budget` shows: today's spend, 7-day rolling
spend, top users by spend, current cap, refused-turn count. v1 is
read-only; raising/lowering the cap happens via the CLI:

```
arizuko budget <inst> set folder <name> --daily 200       # cents
arizuko budget <inst> set user <user_sub> --daily 100
arizuko budget <inst> show folder <name>
```

## Acceptance

1. A channel with `cost_cap_cents_per_day=0` (default) is never
   gated; budget logic runs but no nudges, no stops.
2. A channel with cap=200 cents, current spend 110 cents → next
   turn's envelope carries `<budget_notice level="50">`. The agent
   sees the nudge and can self-throttle.
3. At 80% (160 cents) the agent gets the stronger nudge.
4. At 200 cents the next spawn is refused — operator-visible
   reason logged, user sees a short ephemeral channel reply. No
   LLM call made.
5. Per-user caps compose with channel caps: lower of the two
   thresholds applies.
6. Cost log entries roll forward in `cost_log` per turn; spend
   resets at the configured daily window.
7. `COST_CAPS_ENABLED=false` makes the whole subsystem inert
   (escape hatch).

## Out of scope

- **Per-skill budgets** ("oracle skill is expensive; cap it
  separately"). Future: derive from skill tags or per-MCP-tool cost
  reporting.
- **Monthly / weekly windows.** v1 is daily only; longer windows
  add aggregation complexity for marginal value.
- **Budget gifting / sharing** between users. Caps are per-row;
  no transfers.
- **Forecasting / projections.** Show actual, not projected. If
  operators want trend graphs, the cost_log table is enough source
  data — UI later.
- **Per-call retry on rate-limit-but-under-budget.** That's an
  upstream Anthropic concern, not budget logic.
- **Cost-attribution to skills / tools.** v1 attributes the whole
  turn to the caller, not to specific MCP tools that ran. Per-tool
  attribution would need MCP-tool wrappers measuring their own
  output — separate spec.

## Decisions

- **Daily window, UTC.** Simplest; tz-aware reset is a config
  override.
- **Ephemeral nudges, not history.** Budget notices live in the
  per-turn envelope only — keeps the conversation clean.
- **Hard stop without LLM call.** When cap is hit, the channel
  message comes from gateway directly. We're literally out of
  budget; can't afford to ask the model how to phrase the refusal.
- **Default zero (no cap).** Existing channels/users unaffected.
  Caps are operator opt-in.
- **Cents not dollars.** Integer arithmetic, no float; common SaaS
  pattern.
- **Two warning levels, no more.** muaddib uses one; we add a
  half-warning at 50% for early signal. Three+ would be noise.

## Touches

- `store/migrations/<next>-cost-log.sql` — new table + chats /
  auth_users column adds.
- `store/cost_log.go` (new) — write + aggregate helpers.
- `gateway/budget.go` (new) — per-turn cap check + nudge
  composition.
- `gateway/prompt_build*.go` — append `<budget_notice>` envelope.
  Follows the per-turn ephemeral XML block convention documented in
  `gateway/README.md` ("Per-turn ephemeral XML blocks"); add a row to
  that table when this lands.
- `ipc/submit_turn.go` (or equivalent in `ant/`) — forward token
  counts from Anthropic responses.
- `dashd/budget.go` (new) — `/dash/groups/<folder>/budget`
  read-only view.
- `cmd/arizuko/budget.go` (new) — operator CLI.
- `/etc/arizuko/prices.toml` (new packaging file) — model price
  table.

Out of touch:

- No channel-adapter changes — refusal goes through the normal
  outbound path.
- No new MCP tools.
- No grants changes — caps compose with grants but enforce
  separately.

Estimate: ≈ four daemon-days.
