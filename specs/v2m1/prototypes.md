# Prototypes (Per-JID Group Spawning)

**Status**: not started

## Problem

A support agent handles many users. Each user needs isolated
state — their own memory, working files, conversation context.
But the agent setup (CLAUDE.md, skills, personality) is the same
for all users. Registering a group per user manually doesn't scale.

## Architecture

A **prototype** is a group template that is never routed to
directly. When a new JID needs routing, the gateway spawns a
new group as a copy of the prototype.

```
atlas/support/web               prototype (template, no routing)
atlas/support/web~tg_REDACTED spawned instance (copy)
atlas/support/web~web_abc123    spawned instance (copy)
```

The prototype's CLAUDE.md, skills, and setup are copied to each
spawn. Spawned instances get workdir-level writes — they can
write memory/notes but can't modify the template. The prototype
itself stays clean.

## Spawn naming

Convention: `{prototype}~{sanitized_jid}`

Tilde separator (`~`) chosen because: not `/` (avoids depth
confusion), not `:` (used in JIDs), filesystem-safe, visually
distinct. JID sanitized: replace `:` and `/` with `_`.

## Router integration

When a message arrives for a JID that matches a prototype's
routing rules:

1. Check if spawn `{prototype}~{sanitized_jid}` exists
2. If yes, route to the spawn
3. If no, create spawn: copy prototype dir, register in DB
4. Route to the new spawn

Spawns are registered as real groups in the `groups` table
with a `prototype` column pointing to the template group.
Gateway treats them like normal groups for routing and
container spawning.

```sql
ALTER TABLE groups ADD COLUMN prototype TEXT;
-- NULL = normal group, non-NULL = spawn of that prototype
```

## Spawn lifecycle

- **Created**: on first message matching prototype routing
- **Active**: normal group behavior, own container, own state
- **Idle cleanup**: destroyed after configurable idle timeout
- **Persistent**: optional `persistent: true` flag keeps spawn
  alive across restarts (for long-lived tickets)
- **DB cleanup**: periodic sweep deletes spawn groups with no
  messages in N days

## Spawn limits

- Max concurrent spawns per prototype: configurable via
  `container_config.max_spawns` (default: 50)
- When limit reached: queue new JIDs, or reject with message
- Cleanup frees slots for new spawns

## Filesystem layout

```
groups/
  atlas/support/web/           prototype (template)
    CLAUDE.md
    skills/
    SOUL.md
  atlas/support/web~tg_123/    spawn (copy)
    CLAUDE.md                  copied from prototype
    skills/                    copied from prototype
    SOUL.md                    copied from prototype
    workdir/                   spawn-specific, rw
    .claude/                   spawn-specific memory
```

## Update propagation

New spawns get current prototype state. Existing spawns are
NOT updated — they're isolated copies.

- New conversations get updated template
- Existing conversations keep their version
- To force-update: destroy spawn, next message creates fresh
- No automatic propagation (complexity not worth it)

## Use cases

- Support tickets: each ticket gets its own agent instance
- Public forum: each user conversation is isolated
- Onboarding flows: per-user agent with fresh state
- Any scenario where per-JID state isolation matters

## Tier interaction

Spawns inherit the tier of their prototype. A depth-3
prototype spawns depth-3 workers. Permissions are identical
to the prototype — the `~jid` suffix doesn't change depth
calculation (tilde is not `/`).

## Open Questions

1. **Copy mechanism** — full filesystem copy? Or symlink setup
   files and only create workdir? Symlinks are lighter but
   break if prototype is modified mid-session.

2. **Spawn discovery** — how does the prototype's parent
   (agent tier 2) see its spawns? List via IPC action?
   Aggregate metrics across spawns?

3. **Cross-spawn knowledge** — when one spawn learns something
   (writes a fact), should it be visible to other spawns?
   Shared facts/ mount (ro) from prototype?

4. **Routing rule inheritance** — does the spawn inherit the
   prototype's routing rules? Or is it terminal (no further
   routing)?
