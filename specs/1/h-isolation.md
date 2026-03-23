<!-- trimmed 2026-03-15: TS removed, rich facts only -->

# MCP Tool Isolation

MCP servers in their own docker containers with controlled
communication. Builds on V-sidecars.md.

## settings.json merge order

Agent-written servers < sidecar servers < arizuko (arizuko wins).
Gateway preserves agent-written `mcpServers` on each spawn.

## socat bridge pattern

Agent container uses socat to bridge stdio to unix socket:

```
socat UNIX-CONNECT:/workspace/ipc/sidecars/<name>.sock STDIO
```

socat must be available in the agent image.

## waitForSocket

Gateway polls until unix socket appears (sidecar ready) or times
out (5s). On timeout: log and skip, agent proceeds without sidecar.
Failed probe: sidecar excluded from settings.json.

## Future: gVisor / Firecracker

- **gVisor (runsc)**: drop-in docker runtime (`--runtime=runsc`),
  unix sockets still work
- **Firecracker**: virtio-vsock replaces unix sockets.
  `socat VSOCK-CONNECT:<cid>:7000 STDIO` in agent,
  `socat VSOCK-LISTEN:7000 EXEC:./mcp-server` in microVM.

Transport abstraction keeps sidecar images unchanged.

## Open questions

- Socket cleanup: stale .sock files from crashed sidecars
- Persistent sidecars: expensive-to-start servers (whisper) pooled
  across spawns
- Secret injection: env in SidecarSpec stored in DB (avoid),
  gateway-managed secrets file, or .env interpolation
- Mount allowlist UX: not discoverable, not per-instance,
  verbose format
