---
status: shipped
depends: [E-routd, P-runed, G-engagement]
---

# Proactive interjection

Let the agent speak unprompted when it's useful. arizuko is
mention-reactive: in channels where the bot lurks, valuable
interventions never happen because nothing triggers a turn.

Implementation: `routd/proactive.go`, `routd/db.go:1161-1300`,
`routd/loop.go:403` `maybeScanProactive`. The `proactive:` block is parsed by
`proactive.Parse` — a shared package rather than a routd-internal function,
because `dashd/proactive_page.go` must show the operator the same verdict the
scanner gates on. Operator surfaces: `GET /dash/proactive/` (read-only view)
and `concepts/proactive.html`.

**It ships off.** `PROACTIVE_ENABLED` (`proactive.go:39`) defaults false and
nothing in `template/` sets it, so no instance does this until an operator opts
in twice — once per instance, once per group. Shipped means the mechanism and
both operator surfaces are complete, not that it is running anywhere.

## Decisions

**It is one more input to the existing turn-trigger gate, not a
subsystem.** The same concrete decision already promotes a mention,
sustains an engagement window ([`G-engagement.md`](G-engagement.md)), and
drops an `#observe` message without firing
([`B-route-mode-ingestion.md`](B-route-mode-ingestion.md)). Proactive is
silence-driven where those are message-driven, so it needs a clock —
otherwise it is just another input.

**Driven by routd's loop, not a free-running ticker.** After each loop
iteration, if `now ≥ next_scan_at`, run one scan and advance by
`PROACTIVE_SCAN_INTERVAL`; **no catch-up for missed ticks** — a long turn
just delays the next scan rather than firing a burst afterwards. The
scanner is not started at all when `PROACTIVE_ENABLED` is unset (no
scheduler, not merely an empty body).

**Two floors of suppression, either of which vetoes.** External: the
ordered checks plus the cooldown decide _this moment_ warrants a turn,
before runed is called. Internal: the agent receives the turn and may
emit nothing (`outcome:"silent"`). Agent silence is normal. The two
threats are different — a talkative bot (noise → the team mutes it) and
a drive-by halluciner (low-confidence interjection → trust damage) — so
one floor would not cover both.

**Binary checks, never a weighted score.** Ordered hard vetoes kill the
turn (`QuietHours` → `BotQuiet` → `RecentActivity`, `proactive.go:246`);
then at least one positive signal must arm it. v1 ships exactly one
signal, `UnansweredQuestion`. New signals join the list. A score-sum
would re-create the per-route weighted `impulse_config` engine this repo
deleted (`B-route-mode-ingestion`), which is the whole reason the gate is
concrete.

**Cooldown is mandatory and 24h, per chat.** State-reactive auto-firing
without a cooldown loops; 24h is the arizuko-wide default for this class
(aeon import, 2026-05-22). A channel that had its proactive moment today
does not get another even if a check now passes. No per-mode override — a
halved cooldown would contradict the invariant.

**Silence has two edges.** `gap < PROACTIVE_SILENCE_MIN` means the
conversation is live — don't interrupt. `gap > PROACTIVE_SILENCE_MAX`
means dormant — nobody is there to talk to. Only the band between is a
moment. `last_inbound_at` counts real platform inbounds only: bot replies
and the proactive synthetic row do not reset the clock
(`db.go:1165` `proactiveSelfSender`), or the feature would keep itself
awake.

**Synthetic inbound through the normal loop, not a second dispatch
path.** The fire path appends a `sender="timed-proactive"` row and sets
`proactive_last_fired_at` in **one transaction**, then dispatches
(`db.go:1267`). A crash before dispatch leaves the cooldown set: at worst
one missed turn, never a double-fire. The `timed-` prefix reuses the
existing engagement-skip carve-out, so a proactive turn never extends an
engagement window the user didn't open.

**Mode is group business state in `CLAUDE.md` frontmatter, not a DB
column** — so an operator edit is the single source and there is no
DB/file drift. Cached per group, invalidated on change, never re-parsed
per tick. Cooldown is chat runtime state in `routd.db`
(`chat_proactive`), because it is not something an operator edits.

```yaml
proactive:
  mode: lurk # silent (default) | lurk
  quiet_hours: ['22:00-08:00 Europe/Prague']
```

**Strict, not magical**: no `proactive:` block → `silent`. A
present-but-malformed block (unknown mode, unparseable `quiet_hours`, bad
tz) is a **logged config error** and the group fires nothing — it is
never silently coerced to `silent`, because that would hide the operator's
mistake behind the default.

Instance-wide tuning is env (`PROACTIVE_ENABLED`, `_SCAN_INTERVAL`,
`_SILENCE_MIN`, `_SILENCE_MAX`, `_COOLDOWN`, `_BOT_QUIET`,
`_RECENT_ACTIVITY_MIN`) with defaults in `proactive.go:39-45`.

## Per-turn envelope

One ephemeral `<proactive_reason check="…">` block, appended by the
prompt build. It marks the turn as proactive — the agent reads it as "a
moment to consider speaking", not "a question to answer". `check` is the
firing check's name, the only structured field; the body is freeform
renderer text. Logged at fire time with the chat jid, group, and firing
check — and, on a skip, the vetoing check — so an unwelcome interjection
or a silent no-fire is traceable, never a black-box "the agent decided
to speak".

## Acceptance

1. No `proactive:` block → never fires, whatever the activity.
2. `mode: lurk`, ≥3 inbounds in the last hour, last inbound ends with
   `?`, no bot reply since → exactly one turn; the 24h cooldown blocks a
   second.
3. Agent judges there's nothing to add → no message, `outcome:"silent"`,
   cooldown still set (a considered-but-empty turn counts).
4. Quiet hours veto every turn in the window.
5. `PROACTIVE_ENABLED` unset → no scheduler at all.
6. A proactive turn does not bump engagement; the next user message
   routes exactly as it would have.
7. A malformed block is a logged config error and fires nothing.

## Out of scope

- **A generic event-trigger engine.** The gate carries a small fixed set
  of arizuko's own signals. Custom event-triggering belongs in a
  pre-aggregator running next to arizuko and exposed over MCP — the
  standard extension point — where the agent reaches it like any other
  tool.
- **Cross-channel awareness** ("saw this in #design — speak in #eng?").
  One chat at a time.
- **Operator-defined checks.** Same answer as the first bullet.

Untouched: no adapter change (proactive output takes the normal egress
path), no auth change (`caller_sub="service:routd"`, same permissions as
any turn), no new MCP tool (the agent uses `send`).
