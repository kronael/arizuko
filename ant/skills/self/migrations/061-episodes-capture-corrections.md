# 061 — episodes capture user corrections, not agent conclusions

Rule: user corrections are authoritative, agent conclusions are not.

## What changed

- **`resolve`** — classify/recall/dispatch/act headings are internal.
  Never emit `## Classify`, `Continuation —`, `New task —`. Wrap
  reasoning in `<think>…</think>`. (Fixes marinade Apr 16 leak.)

- **`compact-memories`** — preserve user corrections verbatim, not
  agent summaries. Keep: corrections (quoted), preferences, confirmed
  deliverables, flagged blockers. Drop: conclusions, dead-end debugging,
  routine ops. Example frontmatter leads with `Corrections`.

- **`recall-memories`** — weight corrections over conclusions. Re-derive
  conclusions fresh; never reuse a prior agent summary as fact.

- **`migrate`** — section (e) writes `~/.announced-version` BEFORE the
  broadcast loop so a mid-fanout restart cannot re-announce. Also fixes
  broken `refresh_groups | jq .jid` (tool returns folder, not jid) by
  looking up JIDs from `routes`.

- **`ant/CLAUDE.md`** — `[Document: …]` placeholder with NO `<attachment
  path=…>` means the file did NOT arrive. Do not claim you read it.

## Why

14-day cross-instance audit (sloth/krons/marinade):

- 776 looped bot responses on krons `local:*` — conclusion/recall cycles.
- 22-message migration broadcast storm on sloth (guard written after,
  not before).
- Scaffolding ("## 1. Classify") leaked to marinade Apr 16 — user asked
  "why do you say 1. classivy 4. act wtf?"
- Attachments lost three times before the fourth share worked — agent
  hallucinated that `[Document: …]` alone meant the file was loaded.
