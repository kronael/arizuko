# 061 — episodes capture user corrections, not agent conclusions

Rule: user corrections are authoritative, agent conclusions are not.

- **resolve** — classify/recall/dispatch/act headings are internal.
  Never emit `## Classify`, `Continuation —`, `New task —`. Wrap
  reasoning in `<think>…</think>`.
- **compact-memories** — preserve user corrections verbatim. Keep:
  corrections (quoted), preferences, confirmed deliverables, flagged
  blockers. Drop: conclusions, dead-end debugging, routine ops.
- **recall-memories** — weight corrections over conclusions. Re-derive
  conclusions fresh; never reuse a prior agent summary as fact.
- **migrate** — section (e) writes `~/.announced-version` BEFORE the
  broadcast loop so a mid-fanout restart cannot re-announce. JIDs now
  looked up from the `routes` table (fixes `refresh_groups | jq .jid`
  which returns folder, not jid).
- **ant/CLAUDE.md** — `[Document: …]` without `<attachment path=…>`
  means the file did NOT arrive. Do not claim you read it.
