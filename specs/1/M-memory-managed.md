---
status: shipped
---

# Memory: Managed

Claude Code's built-in persistent memory. No custom code required.
Automatic, always on. Agent controls content entirely.

See `specs/1/K-knowledge-system.md` for memory layer overview.

## What it is

Two file types loaded automatically into every session:

- **CLAUDE.md** — instructions, conventions, behavioural rules
- **MEMORY.md** — agent's own notebook: what it has learned about
  the project, user preferences, patterns, architecture notes

The agent writes both using standard file tools (Write, Edit).
No special MCP, no trigger event — the agent decides when and what
to write, at any point during a session.

## Push (auto-injected)

Both files are injected into the system prompt at session start:

- `CLAUDE.md` — always, in full, as system context
- `MEMORY.md` — first 200 lines always. Lines beyond 200 are not
  loaded; agent is instructed to offload detail into topic files

When `MEMORY.md` is empty, Claude Code shows: _"Your MEMORY.md is
currently empty. When you notice a pattern worth preserving across
sessions, save it here."_ — this is the built-in system instruction
that triggers agent-initiated writes.

## Pull (on demand)

Agent reads topic files alongside `MEMORY.md` using file tools:

```
/home/node/.claude/projects/-workspace-group/memory/
  MEMORY.md          ← 200-line index, always loaded
  debugging.md       ← detail offloaded from MEMORY.md
  patterns.md
  api-conventions.md
  ...
```

No MCP tool needed — agent reads these files directly when it needs
more detail than the 200-line index provides.

## File locations

```
data/sessions/<folder>/.claude/          ← host path
  CLAUDE.md                              ← per-group instructions
  projects/-workspace-group/memory/
    MEMORY.md                            ← auto-memory index
    *.md                                 ← topic files
```

Mounted into container at `/home/node/.claude`. Survive idle timeout,
container restart, and session reset.

Global instance-wide instructions:

```
groups/global/CLAUDE.md                  ← written by main group agent
```

Mounted read-only into non-main groups via
`CLAUDE_CODE_ADDITIONAL_DIRECTORIES_CLAUDE_MD=1`.

## What belongs where

| File                 | Content                                                           |
| -------------------- | ----------------------------------------------------------------- |
| `CLAUDE.md`          | Instructions, conventions, how to behave — authoritative rules    |
| `MEMORY.md`          | Tacit knowledge — preferences, patterns, "how" things work        |
| `facts/<concept>.md` | World facts — "what" things are (v2, see `memory-facts.md`)       |
| `diary/YYYYMMDD.md`  | Time-stamped events — "what happened" (v1, see `memory-diary.md`) |

MEMORY.md is for tacit/behavioural knowledge. Facts are for
concept-centric world knowledge. Diary is for timestamped events.
These three are orthogonal and complementary.

## Enabled by

`CLAUDE_CODE_DISABLE_AUTO_MEMORY: "0"` in `settings.json`, set by
gateway on first container spawn (`buildVolumeMounts` in
`src/container-runner.ts`).

## Open

- **Global MEMORY.md**: main group writes, non-main groups read —
  no equivalent of `global/CLAUDE.md` for memory yet. Would let
  instance-wide patterns persist across groups.
- **Convention enforcement**: agent has no explicit instruction
  distinguishing MEMORY.md (tacit) from facts/ (world knowledge).
  Should be added to `container/CLAUDE.md` so agents follow the split.
- **200-line limit**: agents that write too much to MEMORY.md lose
  context silently. Should the gateway warn or enforce the limit?
- **Topic file discovery**: agent knows to look for topic files only
  if it wrote them. No index of topic files exists. Consider a
  `memory/index.md` convention.
