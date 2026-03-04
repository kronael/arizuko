# Session Recovery

When a session ends abnormally (error_during_execution, error_max_turns),
the new session starts cold — no memory of prior work. The agent cannot
continue without being told what happened.

## Problem

- `error_during_execution`: JSONL is corrupted; new session starts blank
- `error_max_turns`: clean JSONL exists but new session doesn't reference it
- In both cases the user has to re-explain context or say "continue" blindly

## Design

On session eviction or turn-limit hit, gateway generates a **recovery
note** and injects it as the first user message of the new session.

### Recovery note content

```
[session recovery]
Previous session: <sessionId>
Ended: <reason> (error_during_execution | max_turns_reached)

Summary of prior work (extracted from JSONL):
<last N assistant messages, truncated to ~2000 chars>

Pick up where the previous session left off.
```

### How to extract the summary

For `error_max_turns`: the summary query already runs (see agent-runner).
Gateway receives it as an output result — store it and prepend to next
session's prompt.

For `error_during_execution`: read the JSONL, extract the last 3-5
assistant text messages before the error, concatenate as the summary.

### Implementation

Gateway side (`index.ts`):

1. On session eviction, read the old JSONL and extract last assistant texts
2. Store as `recoveryNote` in memory (keyed by group folder)
3. On next `runAgent` call for that group, prepend recovery note to prompt
4. Clear `recoveryNote` after use

Agent-runner side: no change needed — the note arrives as part of the
user prompt, giving Claude full context on first turn.

### Alternative: inject as media

If the JSONL is large, compress and attach as a file reference
(`/workspace/media/recovery-<sessionId>.txt`) rather than inlining.
Agent reads it with `Read` on first turn.

## Constraints

- Recovery note injected once only — not repeated on subsequent messages
- Max inline size: 2000 chars; above that, write to media file
- For `error_during_execution`, skip messages after the error marker
- Do not surface session IDs to the user (internal detail)
