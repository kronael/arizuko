# 008 — isMain → isRoot, global/ → share/

- `ARIZUKO_IS_MAIN` renamed to `ARIZUKO_IS_ROOT`
- `/workspace/global` renamed to `/workspace/share`
- Root group = any single-segment folder (not just `main`)
- `share/` is rw for root groups, ro for children
