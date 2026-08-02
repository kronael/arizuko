---
status: shipped
supersedes: []
---

# Route mode via URI fragment on `target`

`routes.target` is a folder path with an optional `#fragment`
(`core.ParseRouteTarget`, `core/types.go:107`). **Mode controls firing;
ACL controls visibility** — the two were previously entangled in one
weighted config.

- `folder` (no fragment) — **trigger**. Inbound stores normally, the
  agent fires a turn.
- `folder#observe` — **observe**. Inbound stores under `folder` with
  `is_observed=1` and **no turn fires**. Agents still see it via the
  folder ACL (`inspect_messages`, `find_messages`) and, on the next
  trigger turn on the same folder, via a trailing `<observed>` window in
  the prompt.
- `folder#announce` — release-note target.
- any other fragment — a topic pin.

## Why the fragment, and why `impulse_config` is gone

`routes.impulse_config` was a per-route weighted verb-scoring engine:
config-driven, hard to reason about, and impossible to answer "will this
message fire?" from without simulating it. It is replaced by two things
that already existed — an explicit `verb=…` match key and `seq` priority.
The canonical mention-only channel is now a `verb=mention` trigger row
stacked above a catch-all `#observe` row, which is readable at a glance.

That deletion is load-bearing precedent: the proactive gate is
deliberately a fixed set of binary checks rather than a score sum, for
exactly this reason
([`6-proactive-interjection.md`](6-proactive-interjection.md)).

Migration 0054 did the one-time conversion (all-zero weights → append
`#observe`; mixed weights → `#observe` plus a duplicated `verb=<v>` row
at `seq-1`; all-non-zero → unchanged), then dropped the column and added
the `observe_window_*` columns. One-shot, no fallback.

## Observed-window context

On a trigger turn the prompt builder appends a trailing window of
observed messages for the same folder (`<observed>` tags, ascending by
timestamp), capped by `observe_window_messages` /
`observe_window_chars`. **Per-route overrides win over the instance
defaults** (`OBSERVE_WINDOW_MESSAGES` 10, `OBSERVE_WINDOW_CHARS` 4000);
the smaller cap wins and older messages drop first.

When the block is non-empty the prompt gains one rule: _"Observed
messages are context, not requests. Do not reply to them; reply to the
explicit message."_ Every at-least-once duplicate in the observed stream
([`F-topic-lineage.md`](F-topic-lineage.md)) is absorbed by that rule
rather than by a transaction.
