---
status: partial
---

# Session Recovery

When a session ends abnormally (`error_during_execution`,
`error_max_turns`), the new session starts cold. User has to re-explain.

## Design

On eviction or turn-limit hit, gateway generates a **recovery note** and
injects it as the first user message of the next session.

```
[session recovery]
Previous session: <sessionId>
Ended: <reason>

Summary of prior work (extracted from JSONL):
<last N assistant messages, truncated to ~2000 chars>

Pick up where the previous session left off.
```

### Summary extraction

- `error_max_turns`: reuse the summary query already run by agent-runner;
  gateway stores the output and prepends to next session's prompt.
- `error_during_execution`: read JSONL, extract last 3-5 assistant text
  messages before the error marker.

### Implementation (gateway)

1. On eviction, read old JSONL, extract last assistant texts.
2. Store `recoveryNote` keyed by group folder.
3. On next `runAgent` for that group, prepend and clear.

Agent-runner: no change (note arrives as user prompt).

### Alternative for large JSONL

Write `/workspace/media/recovery-<sessionId>.txt`, reference via path.
Agent reads with `Read` on first turn.

## Constraints

- Injected once only.
- Max inline 2000 chars; above that, use media file.
- Skip messages after error marker for `error_during_execution`.
- Do not surface session IDs to user.
