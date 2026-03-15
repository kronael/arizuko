# specs/5 — v2 architecture

13 specs. Mix of shipped decisions and future designs.

## Shipped (decision records)

- [3-agent-teams.md](3-agent-teams.md) — why Agent Teams disabled (orphan/stdio/scoping)
- [A-ipc-mcp-proxy.md](A-ipc-mcp-proxy.md) — MCP over unix socket (shipped as icmcd)

## Future

- [0-agent-code-modification.md](0-agent-code-modification.md) — staging area for agent self-modification
- [2-agent-pipeline.md](2-agent-pipeline.md) — orchestration vs workflows, slink inter-agent
- [6-extend-gateway-self.md](6-extend-gateway-self.md) — root agent modifying gateway code
- [9-identities.md](9-identities.md) — cross-channel identity linking, link codes
- [C-message-mcp.md](C-message-mcp.md) — agent-side message history query tools
- [D-message-wal.md](D-message-wal.md) — write-ahead log for reliable delivery
- [E-plugins.md](E-plugins.md) — agent-proposes/operator-approves plugin system
- [F-prototypes.md](F-prototypes.md) — groups spawned from prototypes on routing miss
- [J-sse.md](J-sse.md) — SSE streams, groups as auth boundary
- [K-topicrouting.md](K-topicrouting.md) — @agent vs #topic, agents as commands
- [M-webdav.md](M-webdav.md) — WebDAV workspace access via Caddy
