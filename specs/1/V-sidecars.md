---
status: shipped
---

# MCP Sidecars

MCP servers in isolated docker containers. Gateway-managed
(operator config via `GroupConfig.Sidecars`). `list_sidecars` and
`configure_sidecar` MCP tools (tier 0-1) persist to `container_config`;
take effect next spawn. Code: `container/sidecar.go`, `ipc/ipc.go`.

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
