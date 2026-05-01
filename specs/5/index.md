---
status: future
---

# specs/5 — agent extensions & workflows

| Spec                                                   | Status    | Hook                                                                |
| ------------------------------------------------------ | --------- | ------------------------------------------------------------------- |
| [2-agent-pipeline.md](2-agent-pipeline.md)             | shipped   | Orchestration (slink) vs workflows (Agent tool)                     |
| [28-mass-onboarding.md](28-mass-onboarding.md)         | shipped   | Self-service onboarding, username=world, web auth gate              |
| [29-acl.md](29-acl.md)                                 | shipped   | Glob-matched user_groups, no operator/user distinction              |
| [30-inspect-tools.md](30-inspect-tools.md)             | shipped   | inspect\_\* MCP family (messages, routing, tasks, session)          |
| [31-autocalls.md](31-autocalls.md)                     | shipped   | Inline fact injection when schema cost > content cost               |
| [32-tenant-self-service.md](32-tenant-self-service.md) | shipped   | Org-chart model: invites, secrets, chats.kind, topic kinds          |
| [9-identities.md](9-identities.md)                     | unshipped | Link one user across multiple platform subs                         |
| [C-message-mcp.md](C-message-mcp.md)                   | shipped   | `get_history` + `get_thread` + `fetch_history` MCP tools            |
| [H-call-llm-mcp.md](H-call-llm-mcp.md)                 | unshipped | Oracle skill — Claude asks codex CLI via subprocess + folder secret |
| [J-sse.md](J-sse.md)                                   | shipped   | SSE + MCP transport on slink tokens; group is auth boundary         |
| [M-webdav.md](M-webdav.md)                             | shipped   | dufs + proxyd JWT/cookie auth, write-block guard                    |
| [N-listener.md](N-listener.md)                         | unshipped | Passive listener group mode + scheduled digest                      |
| [P-operator.md](P-operator.md)                         | note      | Operator is emergent from `**` ACL, not a flag                      |
| [Q-unified-routing.md](Q-unified-routing.md)           | shipped   | Single message table, bare folder JIDs, poll-based outbound         |
| [R-multi-account.md](R-multi-account.md)               | shipped   | Multi-account adapters via multiple service TOMLs                   |
| [S-jid-format.md](S-jid-format.md)                     | unshipped | `platform:account/id` JID, account resolved after Connect           |
| [T-voice-synthesis.md](T-voice-synthesis.md)           | unshipped | `ttsd` + `send_voice` MCP tool                                      |
| [b-ant-standalone.md](b-ant-standalone.md)             | unshipped | Ant standalone — Claude Code distribution + sandbox spawn (CLI)     |
