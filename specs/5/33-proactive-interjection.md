---
status: shipped
depends: [E-routd, P-runed, G-engagement]
---

# Proactive interjection

Let the agent speak unprompted when it's useful. arizuko is
mention-reactive today — the agent answers when called. In channels
where the bot lurks, valuable interventions (a teammate stuck on a
recurring question, a link back to a prior thread) never happen because
nothing triggers a turn.

## Where it runs

The trigger fires **inside routd's orchestration loop** ([`E-routd.md`](E-routd.md)
§ The orchestration loop). routd owns the loop and is the **sole
appender**; runed executes the resulting turn via `POST /v1/runs` and
the agent's `send` callback appends through routd like any other turn.
No new daemon, no Docker handle in routd.

The proactive trigger is **one source into routd's turn-trigger gate** —
the same concrete decision that already promotes a mention, sustains an
engagement window ([`G-engagement.md`](G-engagement.md)), and drops an
`#observe` message without firing ([`B-route-mode-ingestion.md`](B-route-mode-ingestion.md)).
It is silence-driven where those are message-driven, so it needs a timer;
otherwise it is just another input to the same gate, not a parallel
subsystem. The gate stays **concrete** — arizuko's own signals only;
generic event-triggering is out of core (§ Out of scope).

## Why not "just spam the channel"

Two floors of suppression, either of which vetoes a turn — defending
against a talkative bot (noise → team mutes) and a drive-by halluciner
(low-confidence interjection → trust damage):

1. **External** — the ordered checks plus a per-chat cooldown decide
   _this moment_ warrants a turn, before runed is ever called.
2. **Internal** — the agent receives the proactive turn and may emit
   nothing; the run ends `outcome:"silent"`. Agent silence is normal.

Default off; opt-in per folder.

## The proactive sweep

The scanner is **driven by routd's loop**, not a free-running ticker:
after each loop iteration, if `now ≥ next_scan_at`, run one scan and
advance `next_scan_at` by `PROACTIVE_SCAN_INTERVAL` (default 30s) — no
catch-up for missed ticks (a long turn just delays the next scan). The
scanner is **not started at all** unless `PROACTIVE_ENABLED` is set
(unset → no scheduler, not merely an empty body).

Eligible chats are read from a **cached per-group proactive mode**,
populated when a group's `CLAUDE.md` is loaded and invalidated when it
changes — never re-parsed per tick. Only chats whose group mode is
non-`silent` are scanned. For each such `chats` row:

1. **Silence debounce.** `gap = now − last_inbound_at`, where
   `last_inbound_at` is the newest **inbound** `messages` row (a real
   platform message — bot replies and the proactive synthetic row do
   not reset the clock). `gap < PROACTIVE_SILENCE_MIN` (90s) →
   conversation is live, don't interrupt. `gap > PROACTIVE_SILENCE_MAX`
   (12h) → dormant, no one to talk to. Either → skip.
2. **Cooldown** (MANDATORY). `proactive_last_fired_at` on
   `chat_proactive`. If a proactive turn fired within
   `PROACTIVE_COOLDOWN` (default 24h) → skip, regardless of signals.
   **24h is the established arizuko default for any state-reactive
   auto-firing trigger** (aeon import, 2026-05-22): a channel that had
   its proactive moment today does not get another, even if a check now
   passes.
3. **Checks** (ordered, cheap reads over `routd.db` — no agent call, no
   network). **Hard vetoes** run first; any failure skips the chat:
   - `QuietHours` — inside an operator-configured quiet window → skip.
   - `BotQuiet` — the bot spoke within `PROACTIVE_BOT_QUIET` (default
     15m) → skip (don't pile on after just replying).
   - `RecentActivity` — fewer than `PROACTIVE_RECENT_ACTIVITY_MIN`
     (default 3) inbound messages on the chat in the last hour → skip
     (the channel isn't live enough to interject into).

   Then **at least one positive signal** must hold, or skip. v1 ships
   exactly one:
   - `UnansweredQuestion` — the last inbound message's text ends with
     `?` (after trim) and no later bot-authored message exists on the
     chat.

   More signals are added to this list, never via a weighted score. The
   firing check's name + reason is the only output.

4. **Fire.** In one `routd.db` transaction, routd appends the synthetic
   inbound row (`sender="timed-proactive"`, `verb="message"`, empty
   `content` — the `<proactive_reason>` block, not the row body, carries
   the framing) **and** sets `proactive_last_fired_at`. The run is
   dispatched only after that tx commits, so a crash before dispatch
   leaves the cooldown set: at worst one missed proactive turn, never a
   double-fire. Dispatch passes `trigger_sender="timed-proactive"`; the
   `timed-` prefix is the existing engagement-skip carve-out
   ([`E-routd.md`](E-routd.md) § Atomic / ordered-per-chat consistency
   contract — `BumpEngagement` runs unless the trigger starts `timed-`),
   so a proactive turn never extends an engagement window the user
   didn't open. The rendered prompt carries `<proactive_reason>`
   (§ Per-turn envelope).

The sweep runs under routd's single-process concurrency model
([`E-routd.md`](E-routd.md) § Concurrency model). A chat whose folder
has a running or queued turn is skipped — the per-folder queue already
serializes, and a proactive inbound never steers a live turn.

## Schema

One chat-scoped table in `routd.db` (the chat is routd's atomic unit;
[`E-routd.md`](E-routd.md) § routd.db schema). Proactive **mode** is
group-scoped business state, read from the group's `CLAUDE.md`
frontmatter (§ Config) — not a column, so an operator edit is the single
source and there is no DB/file drift.

```sql
CREATE TABLE chat_proactive (
  jid                     TEXT PRIMARY KEY,   -- the chat (chats.jid)
  proactive_last_fired_at TEXT                -- RFC3339Nano UTC, lexically comparable; NULL = never fired
);
```

routd's `routd/migrations/` carries this; no source rows (the table is
new with the feature).

## Config

Per CLAUDE.md's business-vs-infra split:

- **Infra (env, instance-wide).** Tuning + the kill switch — identical
  across folders:

  ```
  PROACTIVE_ENABLED=false      # kill switch; unset/false → no scheduler
  PROACTIVE_SCAN_INTERVAL=30s
  PROACTIVE_SILENCE_MIN=90s
  PROACTIVE_SILENCE_MAX=12h
  PROACTIVE_COOLDOWN=24h
  PROACTIVE_BOT_QUIET=15m
  PROACTIVE_RECENT_ACTIVITY_MIN=3
  ```

- **Business (per group).** Whether a folder participates, and its quiet
  hours — operator data, read from the group's `CLAUDE.md` frontmatter
  (the persona/config carrier, [`E-routd.md`](E-routd.md) prompt build):

  ```yaml
  proactive:
    mode: lurk # silent (default) | lurk
    quiet_hours: ['22:00-08:00 Europe/Prague']
  ```

  Modes: `silent` (default — never fires; existing folders unaffected),
  `lurk` (may interject when the checks pass). `quiet_hours` entries are
  `HH:MM-HH:MM <IANA tz>`; a window may cross midnight; multiple entries
  union. **Strict, not magical**: a folder with no `proactive:` block is
  `silent` (default off). A present-but-malformed block — unknown
  `mode`, unparseable `quiet_hours`, bad tz — is a **logged config
  error**; the group is flagged misconfigured and fires nothing. It is
  never silently coerced to `silent`.

## Per-turn envelope

When a proactive turn fires, routd's prompt build appends one ephemeral
`<proactive_reason>` block (the convention + single-renderer rule:
`routd/README.md` "Per-turn ephemeral XML blocks"). It marks the turn
as proactive — the agent reads it to mean "a moment to consider
speaking", not "a question to answer":

```xml
<proactive_reason check="UnansweredQuestion">
Last inbound "where did we land on the lambda issue?" ends with a
question and has no bot reply.
</proactive_reason>
```

`check` is the firing check's name (the only structured field); the body
is freeform renderer text. Operator-visible (logged at fire time, with
the chat jid, group, firing check, and — on a skip — the vetoing check)
so an unwelcome interjection or a silent no-fire is traceable, never a
black-box "the agent decided to speak."

## Acceptance

1. A folder with no `proactive:` block (the default) never fires,
   regardless of activity.
2. `mode: lurk`, ≥3 inbound messages in the last hour, the last inbound
   ends with `?`, no bot reply since → exactly one proactive turn fires;
   the 24h cooldown blocks a second.
3. The agent judging there's nothing to add emits no output: the channel
   sees no message, the run returns `outcome:"silent"`, the cooldown is
   still set (a considered-but-empty turn counts).
4. Quiet hours (`22:00-08:00`) veto every proactive turn in the window.
5. `PROACTIVE_ENABLED` unset → no scheduler runs (global no-op).
6. A proactive turn does **not** bump engagement
   (`trigger_sender="timed-proactive"` hits the `timed-` skip); the next
   user message routes exactly as it would have.
7. A malformed `proactive:` block is logged as a config error and fires
   nothing — it is not treated as `silent`.

## Out of scope

- **A generic event-trigger engine.** The gate is concrete — arizuko's
  own signals only (mention, engagement, observe, reaction, proactive).
  Custom or general event-triggering is not a core concern: run a
  pre-aggregator next to arizuko and expose it over MCP (the standard
  extension point), and the agent reaches it like any other tool.
  impulse's config-driven weighted engine (per-route `impulse_config`)
  was removed for exactly this reason
  ([`B-route-mode-ingestion.md`](B-route-mode-ingestion.md)); the gate
  carries a small fixed set of signals, never a programmable one.
- **Cross-channel awareness** ("agent saw this in #design — speak in
  #eng?"). One chat at a time.
- **Operator-defined checks.** The v1 set ships built-in; custom signals
  come from the external MCP pre-aggregator (above), not a core config
  knob.

## Decisions

- **Default off, per-folder opt-in.** `silent` until an operator edits
  `CLAUDE.md`. Mode is group business state in the file (single source);
  cooldown is chat runtime state in `routd.db`.
- **Cooldown is mandatory and 24h.** State-reactive auto-firing without
  a cooldown loops; 24h is the arizuko-wide default for this class.
  Per-chat (`chat_proactive.jid`) — one proactive moment per chat per
  day whatever check fired. No per-mode override (a halved cooldown
  would contradict the invariant).
- **Two floors.** External (checks + cooldown) AND internal (agent
  declines → `silent`). Either vetoes.
- **Binary checks, not a weighted score.** Ordered hard vetoes kill the
  turn; one positive signal arms it. No score-sum — that would re-create
  the per-route weighted impulse engine this repo just deleted
  ([`B-route-mode-ingestion.md`](B-route-mode-ingestion.md)).
- **Synthetic inbound, normal loop.** The fire path is a
  `sender="timed-proactive"` row through routd's existing loop — no
  second dispatch path. The `timed-` prefix reuses the engagement-skip
  carve-out (proactive is autonomous, like scheduled `timed-*`).

## Touches

- `routd` orchestration loop — the loop-driven proactive scan, the check
  chain, the atomic synthetic-inbound fire path. routd owns the loop
  ([`E-routd.md`](E-routd.md)); runed is unchanged — a proactive turn is
  an ordinary `POST /v1/runs`.
- routd prompt build — append `<proactive_reason>`; refresh the
  `routd/README.md` "Per-turn ephemeral XML blocks" row (drop the
  stale `score=` attribute, use `check=`).
- `routd/migrations/<next>-chat-proactive.sql` — the `chat_proactive`
  table.
- routd CLAUDE.md-frontmatter reader — parse + cache the `proactive:`
  block (mode + quiet_hours); a parse failure is a logged config error.

Out of touch: no channel-adapter changes (proactive output flows the
normal egress path); no auth/grants changes (same caller permissions as
a regular turn — `trigger_sender="timed-proactive"`,
`caller_sub="service:routd"`); no new MCP tools (the agent uses `send`).
