---
version: 8
description: isMain→isRoot rename, global/→share/ mount path
---

## What changed

- Environment variable `ARIZUKO_IS_MAIN` renamed to `ARIZUKO_IS_ROOT`
- Mount path `/workspace/global` renamed to `/workspace/share`
- Root group = any single-segment folder (not just 'main')
- `share/` is read-write for root groups, read-only for children

## Action required

- Update any scripts checking `ARIZUKO_IS_MAIN` to use `ARIZUKO_IS_ROOT`
- Update file paths from `/workspace/global/` to `/workspace/share/`
- character.json now at `/workspace/share/character.json`
