---
status: shipped
---

# Prototypes

A group's `prototype/` subdirectory defines what its children look like.
When routd resolves a route target that doesn't exist,
`spawnFromPrototype` (`routd/spawn.go`, called from `routd/steer.go`)
copies the parent's `prototype/`, registers the child, and routes to it.

**No prototype flag and no DB column.** Any group with a `prototype/`
subdirectory can spawn children — presence of the directory is the
whole configuration. A boolean column would have been a second source of
truth for something the filesystem already answers.

## What gets copied

`CLAUDE.md` and `PERSONA.md` are **copied, not symlinked**, so a spawn
is independent the moment it exists — editing a parent's prototype must
not retroactively rewrite the character of children already talking to
users. Session, memory, and workdir are not copied (fresh start);
`skills/` is bind-mounted read-only from the parent rather than copied,
because skills are versioned platform content, not per-group character.

## Spawn cap

`Config.MaxChildren`, stored inside the group's `container_config` JSON
blob (`core.GroupConfig`), enforced by `auth.CheckSpawnAllowed`:

- `< 0` — unlimited
- `0` — spawning **disabled** (this is the zero value, so a group is
  born unable to spawn until an operator opts in)
- `n > 0` — at most `n` direct children

When the cap is hit the target falls through to the next route rather
than erroring. A spawn cap exists to bound an inbound flood, and
failing the whole message because the hundredth stranger messaged you
is the wrong failure.

Ordering is load-bearing: containment authorization first, then the cap,
then the write.

## Folder naming

Derived from the triggering JID with `:` → `_` and special characters
stripped (`telegram:-100123456` → `telegram_100123456`).

## Lifecycle

Spawns persist until explicitly removed. No state machine, no
auto-archival — storage is cheap and a wrongly-archived conversation is
not. Spawns inherit the parent's `MIGRATION_VERSION` at creation and run
migrations on boot if behind; existing spawns do not auto-update to a
changed prototype (delete and re-create to refresh).

Routing rules are inherited from the parent. The hierarchy provides
session and data isolation; routing is the parent's to configure.

## Agent-facing spawn is NOT wired

`register_group(fromPrototype=true)` returns
`register_group: fromPrototype not configured`
(`routd/groups_resource.go`). routd's agent socket never wired
`SpawnGroup`/`SetupGroup`, so `register_group` has only ever created the
row + route + git-init — no prototype clone, no skill-skeleton seed —
since the split. The MCP-REST fold preserved that behavior verbatim
rather than quietly changing it.

The automatic path (unrouted target → `spawnFromPrototype`) is the only
live prototype spawn. Wiring the agent-facing one is a feature, not a
bug fix.

## Not in scope

Prototype inheritance across worlds (each world's root defines its own),
spawn or prototype creation from chat commands.
