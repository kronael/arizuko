---
status: active
---

# specs/10 — standalone + reusable

Making each arizuko daemon and capability presentable and usable
standalone, reusable across other agent workloads beyond arizuko.

| Spec                                                 | Status   | Hook                                                            |
| ---------------------------------------------------- | -------- | --------------------------------------------------------------- |
| [b-ant-standalone.md](b-ant-standalone.md)           | deferred | ant as standalone Claude Code distribution; `ant <folder>` CLI  |
| [6-workflows.md](6-workflows.md)                     | deferred | workflowd — TOML flow engine over shared SQLite; agent-agnostic |
| [8-self-eval-skill.md](8-self-eval-skill.md)         | deferred | Self-eval sub-query at container exit; portable skill           |
| [1-multi-agent-commits.md](1-multi-agent-commits.md) | deferred | Committer script for multi-agent git safety (openclaw pattern)  |
| [2-printing-press.md](2-printing-press.md)           | spec     | Integrate printingpress.dev — agent-native CLI generator + MCP. |
