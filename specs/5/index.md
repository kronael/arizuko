---
status: future
---

# specs/5 — agent extensions & workflows

| Spec                                                           | Status     | Hook                                                               |
| -------------------------------------------------------------- | ---------- | ------------------------------------------------------------------ |
| [0-agent-code-modification.md](0-agent-code-modification.md)   | unshipped  | Staging area for root-agent-proposed gateway changes               |
| [2-agent-pipeline.md](2-agent-pipeline.md)                     | shipped    | Orchestration (slink) vs workflows (Agent tool)                    |
| [27-detached-containers.md](27-detached-containers.md)         | unshipped  | Collapse stdout markers onto MCP `submit_turn`; supersedes D       |
| [28-mass-onboarding.md](28-mass-onboarding.md)                 | shipped    | Self-service onboarding, username=world, web auth gate             |
| [29-acl.md](29-acl.md)                                         | shipped    | Glob-matched user_groups, no operator/user distinction             |
| [30-inspect-tools.md](30-inspect-tools.md)                     | shipped    | inspect\_\* MCP family (messages, routing, tasks, session)         |
| [31-autocalls.md](31-autocalls.md)                             | shipped    | Inline fact injection when schema cost > content cost              |
| [32-tenant-self-service.md](32-tenant-self-service.md)         | shipped    | Org-chart model: invites, secrets, chats.kind, topic kinds         |
| [33-auth-landscape.md](33-auth-landscape.md)                   | shipped    | Auth composition mechanics                                         |
| [3-agent-messaging.md](3-agent-messaging.md)                   | unshipped  | Slink links as universal agent-to-agent inboxes                    |
| [6-evangelist.md](6-evangelist.md)                             | superseded | superseded by 6/4-hitl-firewall + 6/5-authoring-product            |
| [6-extend-gateway-self.md](6-extend-gateway-self.md)           | unshipped  | Root agent modifying gateway codebase (plugin dir or agent branch) |
| [9-identities.md](9-identities.md)                             | unshipped  | Link one user across multiple platform subs                        |
| [C-message-mcp.md](C-message-mcp.md)                           | partial    | `get_history` shipped; `get_thread` pending                        |
| [D-message-wal.md](D-message-wal.md)                           | superseded | Superseded by 27 — idempotent `submit_turn(turn_id)` is the WAL    |
| [E-plugins.md](E-plugins.md)                                   | unshipped  | Agent proposes, operator approves plugin system                    |
| [H-call-llm-mcp.md](H-call-llm-mcp.md)                         | unshipped  | `call_llm` tool for non-Claude models via OpenRouter               |
| [J-sse.md](J-sse.md)                                           | partial    | Groups are the SSE auth boundary                                   |
| [M-webdav.md](M-webdav.md)                                     | partial    | dufs + proxyd JWT/cookie auth, write-block guard; Basic Auth todo  |
| [N-listener.md](N-listener.md)                                 | unshipped  | Passive listener group mode + scheduled digest                     |
| [P-operator.md](P-operator.md)                                 | note       | Operator is emergent from `**` ACL, not a flag                     |
| [Q-unified-routing.md](Q-unified-routing.md)                   | partial    | Single message table + router decision point                       |
| [R-ant-go-cli.md](R-ant-go-cli.md)                             | unshipped  | Replace TS ant with Go wrapper around Claude CLI                   |
| [R-multi-account.md](R-multi-account.md)                       | shipped    | Multi-account adapters via multiple service TOMLs                  |
| [S-jid-format.md](S-jid-format.md)                             | unshipped  | `platform:account/id` JID, account resolved after Connect          |
| [T-voice-synthesis.md](T-voice-synthesis.md)                   | unshipped  | `ttsd` + `send_voice` MCP tool                                     |
| [Z-cli-chat.md](Z-cli-chat.md)                                 | unshipped  | `arizuko chat` — interactive terminal agent                        |
| [b-memory-skills-standalone.md](b-memory-skills-standalone.md) | unshipped  | Memory skills as standalone distribution                           |
| [c-agent-services.md](c-agent-services.md)                     | unshipped  | `servd` for agent-declared persistent services                     |
| [d-self-improvement.md](d-self-improvement.md)                 | unshipped  | Scheduled self-eval via timed cron                                 |
