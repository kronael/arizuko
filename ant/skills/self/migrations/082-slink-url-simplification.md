# 082 — slink round-handle URL simplification

The slink round-handle endpoints lost their `/turn/` infix. The
second URL segment after the token IS the round handle.

Before (v0.32.0):

```
GET  /slink/<token>/turn/<id>
GET  /slink/<token>/turn/<id>/status
GET  /slink/<token>/turn/<id>/sse
POST /slink/<token>?steer=<turn_id>
```

After (v0.32.2):

```
GET  /slink/<token>/<id>
GET  /slink/<token>/<id>/status
GET  /slink/<token>/<id>/sse
POST /slink/<token>/<turn_id>          # was ?steer=
```

POST to a turn URL = "extend this round" (steering). POST to bare
`/slink/<token>` = "fresh inject." GET = "observe."

Spec: `specs/1/W-slink.md`. CLI `arizuko send` already uses the
new URL shape; no agent-side change required.

You probably won't notice this — the round-handle protocol is
external (operator scripts, CI, web chat widgets). Mention it if a
user reports their slink URL stopped working with `404`.
