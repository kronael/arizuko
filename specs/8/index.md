---
status: partial
---

# specs/8 — capabilities + infrastructure

Shipped and deferred capabilities: proxy layer, chat UI, workflow
engine, agent self-improvement.

| Spec                                                 | Status   | Hook                                                               |
| ---------------------------------------------------- | -------- | ------------------------------------------------------------------ |
| [2-proxyd.md](2-proxyd.md)                           | shipped  | Public-facing proxy; auth at perimeter, routes to dashd/webd/vited |
| [3-chat-ui.md](3-chat-ui.md)                         | shipped  | webd channel adapter, HTMX UI, slink/SSE, two auth planes          |
| [6-workflows.md](6-workflows.md)                     | deferred | workflowd daemon, TOML flow files over shared SQLite bus           |
| [8-self-eval-skill.md](8-self-eval-skill.md)         | deferred | Self-eval sub-query at container exit                              |
| [1-multi-agent-commits.md](1-multi-agent-commits.md) | deferred | Committer script for multi-agent git safety (openclaw pattern)     |
