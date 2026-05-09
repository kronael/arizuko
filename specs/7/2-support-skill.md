---
status: spec
---

# /support skill — verified-answer orchestrator

A composable skill that answers concrete factual questions with
primary-source citations and tracks multi-turn cases on the same
entity. Lives at `ant/skills/support/`. Used by the support product
(Atlas) and any agent answering domain questions in chat.

## Why

Default agent behaviour on a factual question ("did validator X
trigger Y in epoch Z?") is to reach for the formula or a
training-data guess. The `support` exchange on
`telegram:group/1003805633088` 2026-05-09 14:51–15:18 took five
correction turns to land on what was a single field
(`bidTooLowPenalty.coef`) in
`refs/ds-sam-pipeline/auctions/<epoch>.<slot>/outputs/results.json`.
Failure modes:

- Wrong validator data on initial pull, never quoted back the pubkey
- Single-epoch answer to a range question ("last 10 epochs")
- Derived from formulas instead of reading the recorded outcome
- Source path dump only when explicitly asked
- Voice register absent — generic helpful-assistant tone instead of
  Atlas's dry/punchy SOUL

`/find` (research-mode → writes facts/), `/recall-memories` (memory
grep), `/facts` (notes) cover _research_ and _memory_. None of them
shape "answer in chat with a primary-source citation" — that path is
left to freehand. `/support` fills the gap.

## What

`/support <question>` runs four phases:

1. **Case** — extract entities (pubkeys, epochs, IDs, hashes) and
   match against the prior 3 assistant turns. Same entity, or
   `<reply-to>` to an assistant turn, or correction phrases ("you
   said", "verify", "that's wrong") = continuation; carry the source
   path forward. New entities = new case, reset.
2. **Gather** — open the canonical source for each entity, grep the
   ID, quote the row literally. If the canonical source is unknown,
   `/recall-memories` then `/find`, persist to `~/facts/sources.md`.
   For range queries ("last N", "all X"), enumerate the full range —
   never sample.
3. **Reply** — front-load the answer (one short sentence), then the
   cite (path + field). Render through `~/SOUL.md` Voice section
   before sending. Never "likely / probably / should be" — either
   you read the field or you didn't.
4. **Persist** — new canonical source → `~/facts/sources.md`; user
   correction → `/users`; new general knowledge surfaced → `/find`
   so the next case is one grep away.

Composes existing skills:

| Skill            | Use                                                    |
| ---------------- | ------------------------------------------------------ |
| /recall-memories | Does a fact already answer this? Where's the source?   |
| /find            | Research the canonical source if we don't know it yet  |
| /facts           | Write the new fact after answering (DRY for next case) |
| /users           | Record this user's correction or preference            |
| /oracle          | Second opinion when the recorded source is ambiguous   |

## Continuation rules

Threading by entity ID + correction phrase + reply pointer (the
three signals knowledge-base systems use to thread a ticket).
Concretely:

- Same pubkey/epoch/hash as a prior turn → continuation
- `<reply-to>` to an assistant message → continuation
- Phrases: "verify", "you said", "that's wrong", "no it's", "show
  me", "explain why" — continuation even without a shared entity
- New entity with no shared signal → new case, reset source path

Continuations carry forward: the source path, the cited field, the
prior verdict. Switching source mid-case is only allowed if the user
pointed at a new one explicitly.

## Voice

The skill ends by re-reading `~/SOUL.md` Voice section and
rewriting the reply in that register. SOUL beats the default
markdown-tutorial register that the model defaults to.

## Acceptance

- Atlas 2026-05-09 exchange replayed: correct validator + epoch +
  flagged status in **one turn** instead of five
- Skill ≤ 60 lines (orchestrator only — work delegated to existing
  skills, no duplicated logic)
- `description:` matches factual-question prompts so `/resolve`
  dispatches it on the first turn
- Migration ships the skill in `ant/skills/support/`

## Open

- Source registry format: flat `~/facts/sources.md` vs structured
  `~/facts/sources/<entity-type>.md`. Start flat; split if it grows
  past one screen.
- Confidence / escalation hook for the support product (when to
  forward to a human Telegram group): out of scope for the skill,
  belongs to the product CLAUDE.md.
- Range cap: pasting "all 100 validators" can blow Telegram limits.
  Skill leaves formatting to SOUL voice; product CLAUDE.md decides
  pagination strategy.
