---
status: active
---

# specs/12 — standalone + reusable

Making each arizuko daemon and capability presentable and usable
standalone, reusable across other agent workloads beyond arizuko.

| Spec                                                     | Status     | Hook                                                                                 |
| -------------------------------------------------------- | ---------- | ------------------------------------------------------------------------------------ |
| [b-ant-standalone.md](b-ant-standalone.md)               | draft      | ant as standalone Claude Code distribution; `ant <folder>` CLI                       |
| [c-ant-mcp-runtime.md](c-ant-mcp-runtime.md)             | draft      | Go runtime: MCP front (stream-progress), claude NDJSON driver                        |
| [d-ant-image-cutover.md](d-ant-image-cutover.md)         | draft      | `ant:latest` ENTRYPOINT swap to Go binary; soak protocol                             |
| [6-workflows.md](6-workflows.md)                         | draft      | workflowd — TOML flow engine over shared SQLite; agent-agnostic                      |
| [8-self-eval-skill.md](8-self-eval-skill.md)             | superseded | Same-model `query()` self-eval; superseded by [specs/9/1](../9/1-self-eval-haiku.md) |
| [1-multi-agent-commits.md](1-multi-agent-commits.md)     | draft      | Committer script for multi-agent git safety (openclaw pattern)                       |
| [2-printing-press.md](2-printing-press.md)               | draft      | Integrate printingpress.dev — agent-native CLI generator + MCP.                      |
| [3-template-distillation.md](3-template-distillation.md) | draft      | Harvest live-group wisdom back into `ant/examples/<product>/`.                       |

---

## Queue (from comparative research)

Patterns identified by muaddib / hermes / nanoclaw / openclaw / Anthropic-plugin deep-reads that arizuko genuinely lacks but doesn't yet have a written spec for. See [`tmp/improvements.md`](../../tmp/improvements.md) `## True-gap queue` for the 9 entries:

- NULL-sentinel agent decline
- Pre-container command gate
- Periodic memory/skill nudges
- Versioned PATCH + optimistic concurrency on persona/grants/settings
- Column-flag for per-generation message visibility (steer race persistence)
- `onExit`-callback-chained respawn
- Bidirectional MCP-as-channel triad
- FTS5 over messages
- JSONL durable session log + observer-finalize

Each promotes to its own spec — landing here when cross-cutting / platform-level, or extending an existing spec in `specs/4/`, `specs/5/`, `specs/6/` when the pattern is bucket-specific.
