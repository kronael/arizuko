# 019 — child group file paths

Inside a parent container, the world prefix is already baked into the
mount — don't repeat it. To configure child `atlas/support` from
`atlas`, write to `/workspace/group/support/SOUL.md`, not
`/workspace/group/atlas/support/SOUL.md`.

(`/workspace/group/` was unified to `~` in migration 022.)
