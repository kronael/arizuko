---
status: deferred
---

# specs/6 — products

| Spec                                                   | Status   | Hook                                                               |
| ------------------------------------------------------ | -------- | ------------------------------------------------------------------ |
| [1-multi-agent-commits.md](1-multi-agent-commits.md)   | deferred | Committer script for multi-agent git safety (openclaw pattern)     |
| [2-proxyd.md](2-proxyd.md)                             | shipped  | Public-facing proxy; auth at perimeter, routes to dashd/webd/vited |
| [3-chat-ui.md](3-chat-ui.md)                           | shipped  | webd channel adapter, HTMX UI, slink/SSE, two auth planes          |
| [4-hitl-firewall.md](4-hitl-firewall.md)               | deferred | pending_actions queue + /dash/review for held MCP calls            |
| [5-authoring-product.md](5-authoring-product.md)       | deferred | Author agent template (SOUL + skills), built on HITL               |
| [6-workflows.md](6-workflows.md)                       | deferred | workflowd daemon, TOML flow files over shared SQLite bus           |
| [7-self-learning.md](7-self-learning.md)               | deferred | Skill-guard PreToolUse hook (hermes peel)                          |
| [8-self-eval-skill.md](8-self-eval-skill.md)           | deferred | Self-eval sub-query at container exit                              |
| [9-crackbox-standalone.md](9-crackbox-standalone.md)   | shipped  | egred — forward proxy with per-source allowlists (2026-04-29)      |
| [10-crackbox-arizuko.md](10-crackbox-arizuko.md)       | shipped  | arizuko consumer of egred today; sandd transition planned          |
| [11-crackbox-secrets.md](11-crackbox-secrets.md)       | draft    | egred-based secrets injection at egress                            |
| [12-crackbox-sandboxing.md](12-crackbox-sandboxing.md) | planned  | crackbox `pkg/host/` library for KVM/qemu sandboxing               |
