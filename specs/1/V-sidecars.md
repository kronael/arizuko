<!-- trimmed 2026-03-15: TS removed, rich facts only -->

# MCP Sidecars

MCP servers in isolated docker containers. Two provisioning modes:
gateway-managed (operator config) and agent-requested (IPC action).

## Socket path convention

Three paths for the same unix socket:

```
Host:    data/groups/<group>/.claude/sidecars/<name>.sock
Agent:   /workspace/ipc/sidecars/<name>.sock
Sidecar: /run/socks/<name>.sock
```

Both containers mount the same host socket directory.

## Isolation modes

| Mode           | Files | Network | IPC | Use case             |
| -------------- | ----- | ------- | --- | -------------------- |
| **privileged** | yes   | yes     | yes | full access, trusted |
| **offline**    | yes   | no      | no  | code exec, file proc |
| **web**        | no    | yes     | no  | search, API calls    |

Default: offline (safest). Privileged requires operator allowlist.

## Agent-requested sidecar validation

- Image allowlist: `SIDECAR_ALLOWED_IMAGES=node:22-slim,python:3.12-slim,arizuko-sidecar-*`
- Mount restrictions: relative paths only, under /workspace/group/, default ro
- Resource caps: memoryMb max 1024, cpus max 2.0
- Network requires allowlist
- Max per group: `MAX_SIDECARS_PER_GROUP=4`
- Name format: `^[a-z0-9-]+$`

Lifecycle: agent calls request_sidecar -> gateway validates, spawns,
waits for socket -> returns socket path -> agent connects via socat.
On agent exit: all agent-requested sidecars stopped.

## Open questions

1. Startup latency (5-10s); hot sidecar pooling?
2. Persistence: agent-requested die with agent, gateway-managed persist
3. Sidecar-to-gateway IPC: needs own auth token
4. Image pull: pre-pull or fail fast?
