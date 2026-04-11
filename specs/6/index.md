---
status: draft
---

# specs/6 — web layer

- [1-multi-agent-commits.md](1-multi-agent-commits.md) — git coordination, committer script, adopt/skip analysis
- [2-proxyd.md](2-proxyd.md) `shipped` — proxyd daemon, auth at perimeter, routing to dashd/webd/vited
- [3-chat-ui.md](3-chat-ui.md) `shipped` — webd channel adapter, HTMX UI, slink/SSE, auth planes, JID model
- [4-hitl-firewall.md](4-hitl-firewall.md) `draft` — human-in-the-loop MCP firewall, pending_actions queue, /dash/review
- [5-authoring-product.md](5-authoring-product.md) `draft` — author agent product template (SOUL + skills + system prompt), builds on HITL
- [6-workflows.md](6-workflows.md) `draft` — workflowd daemon, declarative flow files, trigger/step model over shared SQLite bus
- [7-self-learning.md](7-self-learning.md) `draft` — agent-managed memory + skill authoring (hermes peel), phase 1 ships memory/skill_manage MCP tools
