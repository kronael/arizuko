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
- [7-self-learning.md](7-self-learning.md) `draft` — skill-guard hook (hermes peel), threat-pattern scanner blocking malicious writes to ~/.claude/
- [8-self-eval-skill.md](8-self-eval-skill.md) `draft` — self-eval via sub-query at container exit, periodic turn review
