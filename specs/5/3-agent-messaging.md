---
status: unshipped
---

# Agent-to-agent messaging

Slink links (`/pub/s/<token>`) as universal addressable inboxes. Agent
A POSTs to group B's slink with a per-link-token JWT
(`aud=<token>`, `sub=agent:<folder>`); gateway verifies and delivers
like any inbound message with `sender=agent:a`.

Rationale: reuse the slink endpoint and message path rather than build
a separate inter-agent transport.

Unblockers: implement `mint_agent_jwt` IPC/MCP tool; same rate limits
as human senders.
