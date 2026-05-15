---
status: active
---

# specs/5 — agent extensions & workflows

| Spec                                                         | Status  | Hook                                                                                  |
| ------------------------------------------------------------ | ------- | ------------------------------------------------------------------------------------- |
| [2-agent-pipeline.md](2-agent-pipeline.md)                   | shipped | Orchestration (slink) vs workflows (Agent tool)                                       |
| [A-auth-consolidated.md](A-auth-consolidated.md)             | spec    | Folders+routes+acl+identities; the three auth shapes                                  |
| [30-inspect-tools.md](30-inspect-tools.md)                   | shipped | inspect\_\* MCP family (messages, routing, tasks, session)                            |
| [31-autocalls.md](31-autocalls.md)                           | shipped | Inline fact injection when schema cost > content cost                                 |
| [32-tenant-self-service.md](32-tenant-self-service.md)       | shipped | Org-chart model: invites, secrets, chats.kind, topic kinds                            |
| [33-proactive-interjection.md](33-proactive-interjection.md) | spec    | Lurk-mode + validator-chain background loop (muaddib-derived)                         |
| [C-message-mcp.md](C-message-mcp.md)                         | shipped | `get_history` + `get_thread` + `fetch_history` MCP tools                              |
| [H-call-llm-mcp.md](H-call-llm-mcp.md)                       | shipped | Oracle skill — Claude asks codex CLI via subprocess + folder secret                   |
| [J-sse.md](J-sse.md)                                         | shipped | SSE + MCP transport on slink tokens; group is auth boundary                           |
| [M-webdav.md](M-webdav.md)                                   | shipped | dufs + proxyd JWT/cookie auth, write-block guard                                      |
| [P-operator.md](P-operator.md)                               | docs    | Operator = `role:operator` membership in unified ACL (canonical in `ARCHITECTURE.md`) |
| [Q-unified-routing.md](Q-unified-routing.md)                 | shipped | Single message table, bare folder JIDs, poll-based outbound                           |
| [R-multi-account.md](R-multi-account.md)                     | shipped | Multi-account adapters via multiple service TOMLs                                     |
| [S-jid-format.md](S-jid-format.md)                           | shipped | Typed ChatJID/UserJID with kind in path; path.Match globs                             |
| [T-voice-synthesis.md](T-voice-synthesis.md)                 | shipped | `ttsd` + `send_voice` MCP tool                                                        |
| [34-cost-caps.md](34-cost-caps.md)                           | spec    | Per-folder cost ceilings via `cost_log` + Anthropic billing API                       |
| [K-ant-backend-codex.md](K-ant-backend-codex.md)             | planned | Ant `Backend` interface; Codex (`app-server`) as second harness                       |
