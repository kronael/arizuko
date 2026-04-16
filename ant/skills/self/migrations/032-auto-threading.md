# 032 — auto-threading (per-user routing)

Route targets support RFC 6570 expansion: `{sender}` expands to the
sender's file ID (e.g. `atlas/{sender}` → `atlas/tg-123456`). Combined
with `spawnGroupFromPrototype`, each sender auto-gets a child group on
first message.

Setup:

```
groups/atlas/
  prototype/   SOUL.md, CLAUDE.md copied to new children
  support/     fallback

routes: seq=0 target=atlas/{sender} ; seq=1 target=atlas/support
```

`max_children` on the hub caps auto-creation (default 50). Group folders
can now contain `@`, `.`, `+` — only traversal is blocked.
