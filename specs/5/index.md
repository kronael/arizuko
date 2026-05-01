---
status: future
---

# specs/5 — agent extensions & workflows

| Spec                                                           | Status    | Hook                                                                |
| -------------------------------------------------------------- | --------- | ------------------------------------------------------------------- |
| [2-agent-pipeline.md](2-agent-pipeline.md)                     | shipped   | Orchestration (slink) vs workflows (Agent tool)                     |
| [28-mass-onboarding.md](28-mass-onboarding.md)                 | shipped   | Self-service onboarding, username=world, web auth gate              |
| [29-acl.md](29-acl.md)                                         | shipped   | Glob-matched user_groups, no operator/user distinction              |
| [30-inspect-tools.md](30-inspect-tools.md)                     | shipped   | inspect\_\* MCP family (messages, routing, tasks, session)          |
| [31-autocalls.md](31-autocalls.md)                             | shipped   | Inline fact injection when schema cost > content cost               |
| [32-tenant-self-service.md](32-tenant-self-service.md)         | shipped   | Org-chart model: invites, secrets, chats.kind, topic kinds          |
| [33-auth-landscape.md](33-auth-landscape.md)                   | shipped   | Auth composition mechanics                                          |
| [9-identities.md](9-identities.md)                             | unshipped | Link one user across multiple platform subs                         |
| [C-message-mcp.md](C-message-mcp.md)                           | shipped   | `get_history` + `get_thread` + `fetch_history` MCP tools            |
| [E-plugins.md](E-plugins.md)                                   | unshipped | Agent proposes, operator approves plugin system                     |
| [H-call-llm-mcp.md](H-call-llm-mcp.md)                         | unshipped | Oracle skill — Claude asks codex CLI via subprocess + folder secret |
| [J-sse.md](J-sse.md)                                           | partial   | Groups are the SSE auth boundary; round-handle stream added in 7/3  |
| [M-webdav.md](M-webdav.md)                                     | shipped   | dufs + proxyd JWT/cookie auth, write-block guard                    |
| [N-listener.md](N-listener.md)                                 | unshipped | Passive listener group mode + scheduled digest                      |
| [P-operator.md](P-operator.md)                                 | note      | Operator is emergent from `**` ACL, not a flag                      |
| [Q-unified-routing.md](Q-unified-routing.md)                   | shipped   | Single message table, bare folder JIDs, poll-based outbound         |
| [R-ant-go-cli.md](R-ant-go-cli.md)                             | unshipped | Replace TS ant with Go wrapper around Claude CLI                    |
| [R-multi-account.md](R-multi-account.md)                       | shipped   | Multi-account adapters via multiple service TOMLs                   |
| [S-jid-format.md](S-jid-format.md)                             | unshipped | `platform:account/id` JID, account resolved after Connect           |
| [T-voice-synthesis.md](T-voice-synthesis.md)                   | unshipped | `ttsd` + `send_voice` MCP tool                                      |
| [Z-cli-chat.md](Z-cli-chat.md)                                 | unshipped | `arizuko chat` — interactive terminal agent                         |
| [b-memory-skills-standalone.md](b-memory-skills-standalone.md) | unshipped | Ant standalone — Claude Code distribution + sandbox spawn (CLI)     |
| [c-agent-services.md](c-agent-services.md)                     | unshipped | `servd` for agent-declared persistent services                      |
| [d-self-improvement.md](d-self-improvement.md)                 | unshipped | Scheduled self-eval via timed cron                                  |
| [e-replaceability-research.md](e-replaceability-research.md)   | research  | Audit each shipped component against off-the-shelf alternatives     |
