---
status: draft
---

# /support skill — verified-answer orchestrator

A composable skill at `ant/skills/support/` that answers concrete factual
questions with primary-source citations and threads multi-turn cases on the
same entity.

## Why

Default agent behaviour on a factual question is to reach for the formula or
a training-data guess. A real support exchange (telegram, 2026-05-09,
14:51–15:18) took **five correction turns** to land on what was a single
recorded field in a results file. The failure modes, all in one exchange:

- wrong entity pulled on the first try, and the identifier never quoted back
  so the user could catch it;
- a single-epoch answer to an explicit range question ("last 10");
- the value _derived from a formula_ instead of read from the recorded
  outcome;
- the source path produced only after being asked for it;
- generic helpful-assistant register instead of the group's persona voice.

The existing skills cover research (`/find`), memory (`/recall-memories`),
and notes (`/facts`). None of them shapes "answer in chat with a
primary-source citation" — that path is freehand, which is why it failed.

## Shape

`/support <question>` runs four phases:

1. **Case** — extract entities (ids, epochs, hashes) and match against the
   prior assistant turns. Same entity, a reply pointer to an assistant turn,
   or a correction phrase ("you said", "verify", "that's wrong") means
   continuation: carry the source path, cited field, and prior verdict
   forward. A new entity with no shared signal resets the case. Switching
   source mid-case is allowed only if the user pointed at a new one.
2. **Gather** — open the canonical source, grep the id, quote the row
   literally. Unknown source → `/recall-memories`, then `/find`, then
   persist it. Range questions enumerate the full range; never sample.
3. **Reply** — answer first in one short sentence, then the cite (path +
   field). Never "likely / probably / should be": either you read the field
   or you did not.
4. **Persist** — new canonical source to the source registry, user
   correction to `/users`, new general knowledge via `/find`, so the next
   case is one grep away.

It is an orchestrator: `/recall-memories`, `/find`, `/facts`, `/users`, and
`/oracle` do the work; the skill only sequences them and enforces the
citation discipline.

## Acceptance

- The 2026-05-09 exchange replayed lands the correct entity, range, and
  status in **one turn** instead of five.
- The skill stays ≤ 60 lines — orchestration only, no logic duplicated from
  the skills it calls.
- Its `description:` matches factual-question prompts so `/resolve`
  dispatches it on the first turn.

## Open

- Source registry format: flat file vs per-entity-type split. Start flat.
- Escalation to a human (when the support product should hand off) belongs
  to the product's `CLAUDE.md`, not the skill.
- Range cap — enumerating "all 100" can exceed a platform's message limit.
  The skill defers formatting to persona voice; pagination is the product's
  call.
