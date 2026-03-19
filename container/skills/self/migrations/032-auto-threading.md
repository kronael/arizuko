# 032 — Auto-Threading (per-user routing)

Route targets now support RFC 6570 template expansion. A target
containing `{sender}` is expanded per-message to the sender's file ID
(e.g. `atlas/{sender}` → `atlas/tg-123456`).

Combined with `spawnGroupFromPrototype`, this enables per-user
threading: each sender auto-gets their own child group on first message.

## What changed

- `resolveRoute` expands `{sender}` in route targets before returning
- `getJidToFolderMap` derives hub folder from template targets
- `isValidGroupFolder` relaxed — no charset restriction, just traversal safety
- Group folders can now contain `@`, `.`, `+` etc (valid Unix filenames)

## Setup example

```bash
# Hub group with prototype dir for seeding children
groups/atlas/
  prototype/        ← SOUL.md, CLAUDE.md copied to new children
  support/          ← fallback group

# Routes: template first, static fallback
seq=0  type=default  target=atlas/{sender}
seq=1  type=default  target=atlas/support
```

## Impact on agents

- No agent-runner changes
- Auto-created children inherit from hub's `prototype/` dir
- Each child has independent session, memory, diary
- `max_children` on hub caps auto-creation (default 50)
