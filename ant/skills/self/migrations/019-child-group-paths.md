# Migration 019 — Child group file paths

## What changed

The `self` SKILL.md now documents the correct paths for configuring child
groups from inside a parent container.

## Key clarification

`/workspace/group` IS the world folder for the current group. When you are
inside the `atlas` container and want to configure its child group `atlas/support`:

- **Correct**: `/workspace/group/support/SOUL.md`
- **Wrong**: `/workspace/group/atlas/support/SOUL.md` (double-prefixes the world name)

The world prefix is already baked into the mount — do NOT repeat it.

## No action required

This is a documentation-only migration. No files to move or create.
