---
status: spec
---

# Proactive interjection

Let the agent speak unprompted when it's useful. Today arizuko is
strictly mention-reactive — the agent answers when it's called. In
public channels where the bot lurks, valuable interventions
(noticing a teammate is stuck, recognizing a recurring question,
linking back to a prior thread) never happen because nothing
triggers them.

Adopted from muaddib's `src/rooms/command/proactive.ts`, which has
run on IRC since July 2025 and is the most operationally-validated
piece of that project. The mechanism is small (debounce + validator
chain + score threshold + mode gate); arizuko has nothing in this
slot today.

## Why not "just spam the channel"

Two failure modes a naive design would hit:

1. **Talkative bot** — the agent interjects on every silence, every
   topic shift, every reaction. Channel becomes noise; team mutes.
2. **Drive-by halluciner** — the agent fires off a half-confident
   "I notice you might want to…" with no grounding. Damages trust
   faster than anything else.

The defensive structure: a chain of independent validators must all
agree that _this specific moment_ warrants a turn, and the channel
must explicitly be in a mode that allows proactive output. Default
off; opt-in per channel.

## Mechanism

A new background loop in `gateway/` (provisional name
`gateway/proactive.go`) scans active channels at a low cadence
(every ~30s). For each channel:

1. **Mode gate.** Read the channel's `mode` (new column on
   `chats` or a row in the existing `chat_modes` shape if one
   exists; see Schema below). Modes: `silent` (default — no
   proactive turns), `lurk` (agent observes; may interject if
   validators pass), `active` (agent participates freely, validators
   still gate interjection cadence). If `silent` → skip.
2. **Silence debounce.** Compute `now - last_message_at`. If less
   than the configured `proactive.silence_min` (default 90s) — agent
   shouldn't interrupt active conversation — skip. If greater than
   `proactive.silence_max` (default 12h) — channel is dormant, no
   one to talk to — skip.
3. **Validator chain.** Run a sequence of cheap validators in order.
   Each returns a score in `[0, 1]` and a one-line reason. Validators
   are pluggable (per-channel `CLAUDE.md` may opt some in/out); the
   v1 set:
   - `RecentActivityValidator` — did at least N messages land in the
     last hour? (otherwise the channel isn't really live)
   - `UnansweredQuestionValidator` — does the last message read like
     a question that didn't get an answer?
   - `RecurringTopicValidator` — does the recent thread match a
     pattern that the per-user memory or channel diary has notes
     on?
   - `MentionGapValidator` — has the bot been silent for
     `proactive.bot_quiet_min`? (don't carpet-bomb after just
     answering)
   - `SchedulerSilenceValidator` — is the channel in a time window
     where unsolicited messages would be rude? (operator-config'd
     quiet hours)
4. **Score threshold.** Sum / aggregate validator scores; require
   total ≥ `proactive.score_threshold` (default 0.7). Lower bar in
   `active` mode; raise it in `lurk`.
5. **Cooldown.** Per-channel `proactive_last_fired_at`. If a turn
   has fired in the last `proactive.cooldown_min` (default 30min),
   skip regardless of score. Prevents loops.
6. **Fire.** Build a normal inbound message with
   `Sender = "proactive"`, route through the gateway just like any
   other inbound. The agent gets the recent channel context plus a
   per-turn envelope flag (`<proactive_reason>...</proactive_reason>`
   with the top-scoring validator's reason) so it knows this isn't
   a question to answer but a moment to consider whether to speak.

The agent decides whether to actually emit text. If it judges
nothing useful to say, it emits nothing — the proactive turn ends
silently. (This is the "second floor" — even if validators agree
externally, the agent agrees internally before output appears.)

## Schema

Additive migration:

```sql
ALTER TABLE chats
  ADD COLUMN proactive_mode TEXT NOT NULL DEFAULT 'silent';
ALTER TABLE chats
  ADD COLUMN proactive_last_fired_at TEXT;
```

Modes: `silent` | `lurk` | `active`. Default `silent` so existing
channels are unaffected.

Optional companion table for per-channel validator overrides (not
required v1 — channel CLAUDE.md can carry these):

```sql
CREATE TABLE proactive_overrides (
  folder TEXT NOT NULL,
  validator TEXT NOT NULL,
  enabled INTEGER NOT NULL DEFAULT 1,
  weight REAL NOT NULL DEFAULT 1.0,
  PRIMARY KEY (folder, validator)
);
```

## Configuration

Per-instance defaults in `.env`:

```
PROACTIVE_ENABLED=true                       # global kill switch
PROACTIVE_SILENCE_MIN=90s
PROACTIVE_SILENCE_MAX=12h
PROACTIVE_COOLDOWN_MIN=30m
PROACTIVE_SCORE_THRESHOLD=0.7
PROACTIVE_BOT_QUIET_MIN=15m
PROACTIVE_SCAN_INTERVAL=30s
```

Per-channel overrides via `CLAUDE.md` frontmatter (operator edits):

```yaml
proactive:
  mode: lurk
  threshold: 0.8 # tighter than default
  quiet_hours: ['22:00-08:00 Europe/Prague']
  validators:
    UnansweredQuestionValidator: enabled
    RecurringTopicValidator: disabled
```

## Per-turn envelope

When a proactive turn fires, the gateway appends to the turn
envelope:

```xml
<proactive_reason validator="UnansweredQuestionValidator" score="0.82">
Last message "where did we land on the lambda issue?" reads like an
unanswered question; cold-start runbook has a recent matching
entry.
</proactive_reason>
```

The agent reads this and decides: emit a citation-grounded reply,
or stay silent. The reason is operator-visible (logged) so
unwelcome interjections can be traced to which validator fired.

## Operator dashboard

`/dash/groups/<folder>/proactive` — read-only listing of: current
mode, last-fired-at, recent validator scores (last N firings or
considered-but-skipped events), enabled-validator list. A toggle
that flips `proactive_mode` between values lands in v2; v1 is
file-only (CLAUDE.md frontmatter).

## Acceptance

1. A channel with `proactive_mode='silent'` (the default) never
   fires a proactive turn, regardless of activity.
2. A channel with `proactive_mode='lurk'`, 5 minutes of silence
   after a question, no bot reply yet → exactly one proactive turn
   fires; cooldown blocks a second one for 30 minutes.
3. The agent receiving a proactive turn and judging there's nothing
   to add emits no output. The channel sees no message; gateway
   logs the silent termination.
4. A validator can be disabled per-channel via CLAUDE.md
   frontmatter; the chain runs without it.
5. Quiet hours (`22:00-08:00`) suppress all proactive turns inside
   the window, even with `mode=active`.
6. Forced disable: `PROACTIVE_ENABLED=false` makes the scan loop a
   no-op globally (escape hatch for any operator who wants to be
   sure).

## Out of scope

- **Multi-channel coordination.** Proactive turns fire
  per-channel; no cross-channel awareness ("agent saw a similar
  question in #design — should it speak in #eng?"). Maybe later;
  one channel at a time first.
- **Learning the threshold.** Per-channel score-threshold tuning
  via feedback signals (operator reactions, user replies) — a
  feedback-loop spec on its own. v1 uses operator-set values.
- **External validators.** Custom validators in user skills (e.g.
  `RunbookMatchValidator` defined by an operator). Hooks for this
  exist via per-channel CLAUDE.md but the validator set itself
  ships built-in.
- **Active mode without channel admin opt-in.** No way to flip a
  channel into `active` without operator-edited CLAUDE.md.

## Decisions

- **Default off.** Channels start in `silent` mode. Operators
  explicitly opt in per-channel by editing `CLAUDE.md`.
- **Two floors of suppression.** External (validator chain
  agreement) AND internal (agent decides whether to emit). Either
  can veto a turn. The agent's silence is normal and accepted.
- **Score threshold not vote count.** Validators contribute scores,
  not yes/no votes. A strong signal from one validator can pass
  threshold without consensus; a weak signal from many can't.
- **Per-turn envelope carries the reason.** Operator-visible,
  audit-friendly. No black-box "the agent decided to speak."
- **Cooldown per-channel, not per-validator.** A channel that's
  already had its proactive moment recently doesn't get another
  one even if a different validator now scores high. Avoids
  rapid-fire interjection.
- **Adopt muaddib's design, not their code.** Their TS doesn't
  port — we re-implement in Go in `gateway/`. Reference:
  `refs/muaddib/src/rooms/command/proactive.ts`.

## Touches

- `gateway/proactive.go` (new) — background loop, validator chain,
  per-channel state.
- `gateway/proactive_validators.go` (new) — the five v1 validators
  as `Validator` interface implementations.
- `gateway/prompt_build*.go` — append `<proactive_reason>` envelope
  block on proactive turns. Follows the per-turn ephemeral XML block
  convention documented in `gateway/README.md` ("Per-turn ephemeral
  XML blocks"); add a row to that table when this lands.
- `store/migrations/<next>-proactive-mode.sql` — additive columns.
- `core/types.go` — `ProactiveMode` enum + parsing.
- `dashd/proactive.go` (new) — read-only `/dash/groups/<folder>/proactive`.
- `gateway/persona.go` — read `proactive:` block from CLAUDE.md
  frontmatter; pass into the validator chain config.

Out of touch:

- No channel-adapter changes — proactive turns flow through the
  normal outbound path.
- No grants/auth changes — same caller permissions as a regular
  agent turn.
- No new MCP tools — agent uses existing `send` tool.

Estimate: ≈ one daemon-week.
