---
status: active
---

# specs/5 — agent extensions & workflows

| Spec                                                         | Status  | Hook                                                                |
| ------------------------------------------------------------ | ------- | ------------------------------------------------------------------- |
| [2-agent-pipeline.md](2-agent-pipeline.md)                   | shipped | Orchestration (slink) vs workflows (Agent tool)                     |
| [28-mass-onboarding.md](28-mass-onboarding.md)               | shipped | Self-service onboarding, username=world, web auth gate              |
| [29-acl.md](29-acl.md)                                       | shipped | Glob-matched user_groups, no operator/user distinction              |
| [30-inspect-tools.md](30-inspect-tools.md)                   | shipped | inspect\_\* MCP family (messages, routing, tasks, session)          |
| [31-autocalls.md](31-autocalls.md)                           | shipped | Inline fact injection when schema cost > content cost               |
| [32-tenant-self-service.md](32-tenant-self-service.md)       | shipped | Org-chart model: invites, secrets, chats.kind, topic kinds          |
| [33-proactive-interjection.md](33-proactive-interjection.md) | spec    | Lurk-mode + validator-chain background loop (muaddib-derived)       |
| [C-message-mcp.md](C-message-mcp.md)                         | shipped | `get_history` + `get_thread` + `fetch_history` MCP tools            |
| [H-call-llm-mcp.md](H-call-llm-mcp.md)                       | shipped | Oracle skill — Claude asks codex CLI via subprocess + folder secret |
| [J-sse.md](J-sse.md)                                         | shipped | SSE + MCP transport on slink tokens; group is auth boundary         |
| [M-webdav.md](M-webdav.md)                                   | shipped | dufs + proxyd JWT/cookie auth, write-block guard                    |
| [P-operator.md](P-operator.md)                               | docs    | Operator is emergent from `**` ACL — canonical in `ARCHITECTURE.md` |
| [Q-unified-routing.md](Q-unified-routing.md)                 | shipped | Single message table, bare folder JIDs, poll-based outbound         |
| [R-multi-account.md](R-multi-account.md)                     | shipped | Multi-account adapters via multiple service TOMLs                   |
| [S-jid-format.md](S-jid-format.md)                           | shipped | Typed ChatJID/UserJID with kind in path; path.Match globs           |
| [T-voice-synthesis.md](T-voice-synthesis.md)                 | shipped | `ttsd` + `send_voice` MCP tool                                      |
