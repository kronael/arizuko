# 061 — episodes capture user corrections, not agent conclusions

Three skills tightened to reflect a simple rule: the agent's judgement
is unreliable, the user's corrections are authoritative.

## What changed

- **`resolve`** — classify / recall / dispatch / act section headings
  are internal only. Never emit `## Classify`, `Continuation —`, or
  `New task —` to the user. Wrap reasoning in `<think>…</think>`.
  (Fixes: marinade Apr 16 scaffolding leak.)

- **`compact-memories`** — episode purpose is to preserve user
  corrections verbatim, not agent summaries. Keep: corrections (quoted),
  preferences stated, confirmed deliverables, flagged blockers. Drop:
  agent-drawn conclusions, dead-end debugging, routine ops. New example
  frontmatter shows `Corrections` section leading.

- **`recall-memories`** — weight corrections over conclusions. Re-derive
  conclusions fresh each time; never reuse a prior agent summary as a
  fact.

- **`migrate`** — section (e) now writes `~/.announced-version` BEFORE
  the broadcast loop, not after, so a mid-fanout container restart
  cannot re-announce the whole release. Also fixes the broken
  `refresh_groups | jq .jid` pseudocode (the MCP tool returns folder,
  not jid) by looking up JIDs from the `routes` table.

- **`ant/CLAUDE.md`** — attachment section now explicitly says: if
  the message has a `[Document: …]` placeholder with NO `<attachment
  path=…>` tag, the file did NOT arrive. Do not claim you read it.

## Why

From the 14-day cross-instance audit (sloth/krons/marinade):

- 776 looped bot responses on krons `local:*` groups — confused by
  conclusion/recall cycles.
- 22-message migration broadcast storm on sloth (no announced-version
  guard triggered because it was written after, not before).
- Skill template scaffolding ("## 1. Classify") leaked to user on
  marinade Apr 16 — user literally asked "why do you say 1. classivy
  4. act wtf?"
- Attachments lost three times before fourth share worked, because
  the agent hallucinated that `[Document: …]` alone meant the file
  was loaded.
