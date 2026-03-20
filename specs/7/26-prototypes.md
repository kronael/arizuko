# Prototypes

**Status**: shipped (spawn and copy work; cleanup job pending)

A group's `prototype/` subdirectory defines what its children look
like. When a child is spawned (via auto-threading or onboarding),
the parent's `prototype/` is copied into the new child folder.

## Model

```
groups/root/prototype/          what new worlds look like
groups/atlas/prototype/         what atlas children look like
groups/atlas/support/prototype/ what support children look like
```

When gated resolves a route target that doesn't exist,
`spawnFromPrototype` copies the parent's `prototype/` dir,
registers the child in DB, and routes to it.

```
gated resolves target "main/support/tg_123"
  → target doesn't exist
  → copy from "main/support/prototype/"
  → register child in DB
  → route to child
```

No special prototype flag or DB column. Any group with a
`prototype/` subdirectory can spawn children.

## What gets copied

- `CLAUDE.md`, `SOUL.md` — full copy (not symlink). Spawns are
  independent once created.
- Session, memory, workdir — NOT copied (fresh start)
- DB state — new row, empty session
- `skills/` — mounted read-only from parent (not copied)

## Spawn limits

`max_children` on the parent group (default: 50). When reached, new
targets fall through to the next route (fallback, not error).

```sql
-- groups table
max_children INTEGER DEFAULT 50  -- 0 = no spawning
```

## Spawn folder naming

Derived from the triggering JID. Colon replaced by underscore,
special chars stripped:

```
telegram:-100123456   → telegram_100123456
discord:98765         → discord_98765
```

## Filesystem

```
groups/
  root/
    prototype/          what new worlds look like
      CLAUDE.md
      SOUL.md
  main/
    support/            parent group (has prototype/)
      prototype/
        CLAUDE.md
        SOUL.md
      tg_123/           spawn (child)
        CLAUDE.md       copied from support/prototype/
        SOUL.md         copied from support/prototype/
```

`template/` at repo root seeds `groups/root/prototype/` on
`arizuko create`. It is the initial definition of what new worlds
look like.

## Thread lifecycle

Spawn groups have three states:

- **active** — normal routing and processing
- **closed** — no new messages accepted, falls through to next route.
  Folder preserved for archival reads.
- **archived** — folder compressed, moved to `groups/<parent>/archive/`,
  DB row removed.

Config on parent group:

```
spawn_ttl_days       INT  default 7   -- mark closed after N days inactive
archive_closed_days  INT  default 1   -- archive closed after N days
```

Cleanup runs once per day via timed daemon. Note: `spawn_ttl_days`
cleanup and archive jobs are not yet implemented — spawns persist
active indefinitely regardless of inactivity.

## Migrations

Spawns inherit the parent's `MIGRATION_VERSION`. On boot, if spawn
version < parent version, agent runs migrations from
`skills/self/migrations/`. New spawns get current parent state.
Existing spawns don't auto-update — delete and re-create to refresh.

## Routing inheritance

Spawns inherit routing rules from the parent. The hierarchy provides
session and data isolation — routing is fixed by the parent's config.

## Acceptance criteria

1. `spawnFromPrototype` copies parent's `prototype/` to child folder
2. `CLAUDE.md`, `SOUL.md` copied; `skills/` bind-mounted read-only
3. `max_children` enforced; fallback to next route when exceeded
4. Folder names derived from JID (`spawnFolderName(jid string) string`)
5. Cleanup job marks inactive spawns closed, archives after threshold

## Not in scope

- Prototype inheritance across worlds (each world's root defines its own)
- Spawn creation from chat commands (use auto-threading routes)
