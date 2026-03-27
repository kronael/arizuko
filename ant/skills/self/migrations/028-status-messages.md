# 028 — Status Messages

The agent-runner now supports `<status>text</status>` blocks. When you emit
a status block during a long-running task, the runner strips it from your
final output and sends it immediately as an interim update to the user
(prefixed with hourglass emoji).

## What changed

- `<status>` blocks are extracted after `<think>` blocks are stripped
- Each status fires an immediate message to the channel
- The old 100-message mechanical heartbeat has been removed
- Status updates are agent-initiated: deliberate and meaningful

## What to do

Use `<status>text</status>` in your output for long tasks:

```
<status>searching facts for antenna models…</status>
<status>reading 12 files, synthesising…</status>
```

Keep status text short (one line, under 100 chars). Multiple blocks are
fine — each sends an immediate update. See `~/.claude/CLAUDE.md` section
"Status Updates" for details.

No action required — this is informational. The feature works automatically
when you emit `<status>` blocks.
