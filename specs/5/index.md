---
status: future
---

# specs/5 — agent extensions & workflows

| Spec                                                           | Status     | Hook                                                               |
| -------------------------------------------------------------- | ---------- | ------------------------------------------------------------------ |
| [0-agent-code-modification.md](0-agent-code-modification.md)   | unshipped  | Staging area for root-agent-proposed gateway changes               |
| [2-agent-pipeline.md](2-agent-pipeline.md)                     | partial    | Orchestration (slink) vs workflows (Agent tool)                    |
| [27-detached-containers.md](27-detached-containers.md)         | unshipped  | File-based output + reclaim for gated-restart survival             |
| [3-agent-messaging.md](3-agent-messaging.md)                   | unshipped  | Slink links as universal agent-to-agent inboxes                    |
| [3-agent-teams.md](3-agent-teams.md)                           | superseded | Claude Code Agent Teams disabled; subagents stay                   |
| [5-agent-media-awareness.md](5-agent-media-awareness.md)       | unshipped  | Teach agent to Read attached PDFs/images                           |
| [6-evangelist.md](6-evangelist.md)                             | deferred   | Community-engagement agent (scrape → score → draft → review)       |
| [6-extend-gateway-self.md](6-extend-gateway-self.md)           | unshipped  | Root agent modifying gateway codebase (plugin dir or agent branch) |
| [9-identities.md](9-identities.md)                             | unshipped  | Link one user across multiple platform subs                        |
| [C-message-mcp.md](C-message-mcp.md)                           | unshipped  | `get_history` / `get_thread` MCP tools                             |
| [D-message-wal.md](D-message-wal.md)                           | unshipped  | WAL for reliable pipe-to-container message delivery                |
| [E-plugins.md](E-plugins.md)                                   | unshipped  | Agent proposes, operator approves plugin system                    |
| [G-agent-backends.md](G-agent-backends.md)                     | superseded | Codex / Pi as ant backends — not shipping                          |
| [H-call-llm-mcp.md](H-call-llm-mcp.md)                         | unshipped  | `call_llm` tool for non-Claude models via OpenRouter               |
| [J-sse.md](J-sse.md)                                           | partial    | Groups are the SSE auth boundary                                   |
| [M-webdav.md](M-webdav.md)                                     | shipped    | dufs + proxyd Basic Auth, per-group workspace over WebDAV          |
| [N-listener.md](N-listener.md)                                 | unshipped  | Passive listener group mode + scheduled digest                     |
| [P-operator.md](P-operator.md)                                 | unshipped  | Proactive operator agent for cross-group events                    |
| [Q-unified-routing.md](Q-unified-routing.md)                   | partial    | Single message table + router decision point                       |
| [R-ant-go-cli.md](R-ant-go-cli.md)                             | unshipped  | Replace TS ant with Go wrapper around Claude CLI                   |
| [R-multi-account.md](R-multi-account.md)                       | shipped    | Multi-account adapters via multiple service TOMLs                  |
| [S-jid-format.md](S-jid-format.md)                             | unshipped  | `platform:account/id` JID, account resolved after Connect          |
| [T-voice-synthesis.md](T-voice-synthesis.md)                   | unshipped  | `ttsd` + `send_voice` MCP tool                                     |
| [Z-cli-chat.md](Z-cli-chat.md)                                 | unshipped  | `arizuko chat` — interactive terminal agent                        |
| [a-crackbox-sandboxing.md](a-crackbox-sandboxing.md)           | deferred   | QEMU/KVM resident-VM sandboxing via crackbox                       |
| [b-memory-skills-standalone.md](b-memory-skills-standalone.md) | unshipped  | Memory skills as standalone distribution                           |
| [c-agent-services.md](c-agent-services.md)                     | unshipped  | `servd` for agent-declared persistent services                     |
| [d-self-improvement.md](d-self-improvement.md)                 | unshipped  | Scheduled self-eval via timed cron                                 |
