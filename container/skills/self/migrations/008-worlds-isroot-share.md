---
version: 8
description: isMain→isRoot rename, global/→share/ mount path
---

## What changed

- Environment variable `NANOCLAW_IS_MAIN` renamed to `NANOCLAW_IS_ROOT`
- Mount path `/workspace/global` renamed to `/workspace/share`
- Root group = any single-segment folder (not just 'main')
- `share/` is read-write for root groups, read-only for children

## Action required

- Update any scripts checking `NANOCLAW_IS_MAIN` to use `NANOCLAW_IS_ROOT`
- Update file paths from `/workspace/global/` to `/workspace/share/`
- character.json now at `/workspace/share/character.json`
