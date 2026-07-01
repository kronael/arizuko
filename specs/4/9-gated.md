---
status: superseded
superseded_by: [5/E-routd, 5/P-runed, 5/1-auth-standalone]
---

# gated

> **Superseded.** The `gated` gateway daemon was deleted 2026-06-07
> when the monolith split into three plane-owning daemons, each
> owning its own DB: [`specs/5/E-routd.md`](../5/E-routd.md) (routing
> rules + message/event store + orchestration loop + channel
> ingress/egress, `routd.db`), [`specs/5/P-runed.md`](../5/P-runed.md)
> (work queue + container lifecycle + per-tenant MCP socket + token
> brokering, `runed.db`), and
> [`specs/5/1-auth-standalone.md`](../5/1-auth-standalone.md) (OAuth,
> JWT signer, JWKs, `auth.db`). This file is kept for history; read
> those three for the live model.
