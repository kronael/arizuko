---
status: shipped
---

# Session Recovery

Handled at the skill layer. The `recall-messages` / `diary` skills let
the agent summarize the prior session when context seems missing —
`diary.WriteRecovery()` writes a note, and the next session's agent
reads it via the diary injection path.

No gateway-level JSONL scraping. Session continuity survives via:

- SDK session resume (2-day idle window) — see `3/E-memory-session.md`
- Diary summaries — see `1/L-memory-diary.md`
- Recall skill — on-demand message lookup by the agent

## Unblockers (if deeper recovery ever needed)

File-based recovery notes, persisted per group, read once on
next-session start. Would layer on top of the skill flow.
