---
status: superseded
---

# Daemon genericization (superseded)

Realized as concrete daemon specs; this file is kept as a stub so its
inbound links still resolve.

- The `gated` split is three daemons: **`5/1` authd** (extracted
  first, in its own release), then **`5/E` routd** + **`5/P` runed**
  in one big-bang cutover (MCP host folded into `runed`).
  See [`1-auth-standalone.md`](1-auth-standalone.md),
  [`E-routd.md`](E-routd.md), [`P-runed.md`](P-runed.md).
- The orthogonal sibling-component pattern moved to
  [`11/A-orthogonal-components.md`](../11/A-orthogonal-components.md).
- The durable conventions (daemon naming, DAG library layering,
  `types/` shared-IDs package, per-daemon `<daemon>/api/v1/` contract,
  DB-ownership rule, NO BACKWARD COMPATIBILITY) moved to
  `ARCHITECTURE.md` § _Daemon genericization conventions_ — they are
  platform architecture, not a proposal.
- The `ContainerRuntime` pluggable-backend design lives with the
  daemon that owns per-spawn lifecycle: [`P-runed.md`](P-runed.md).
