---
status: draft
depends: 9-crackbox-standalone
---

# Crackbox integration into arizuko

> Use crackbox library for network-isolated agent execution in arizuko.

## Problem

arizuko's `container/runner.go` spawns Docker containers with unrestricted
network access. MCP tools can exfiltrate data or call unauthorized APIs.

## Solution

Import crackbox as a Go library. Replace `docker run` with `iso.Run()`.

```go
// gated/main.go — at startup
iso := crackbox.New(crackbox.Config{
    Backend:     crackbox.DockerBackend{},
    ProxyAddr:   "10.99.0.1:3128",
    Subnet:      "10.99.0.0/16",
})

// container/runner.go — when spawning
iso.Run(crackbox.RunOpts{
    Name:      containerName,
    Image:     cfg.Image,
    Mounts:    mounts,
    Env:       secrets,
    Allowlist: allowlistForGroup(in.Folder),
})
```

## Allowlist source

New `network_rules` table (per-group domain/IP allowlist):

```sql
CREATE TABLE network_rules (
    folder     TEXT NOT NULL,
    target     TEXT NOT NULL,  -- domain or IP/CIDR
    created_at DATETIME NOT NULL,
    PRIMARY KEY (folder, target)
);
```

## MCP tools

`request_network(target)` and `list_network_rules()`. Tier-based auto-approval:

| Tier      | Behavior                                       |
| --------- | ---------------------------------------------- |
| 0 (root)  | Auto-approve all                               |
| 1 (world) | Auto-approve known domains (anthropic, github) |
| 2+        | Require human approval                         |

## Default allowlist

All containers get:

```
anthropic.com
api.anthropic.com
```

Per-group additions come from `network_rules` table.

## Inheritance

Child groups inherit parent's allowlist:

```
atlas/                  → [anthropic.com]
atlas/support/          → [anthropic.com, zendesk.com]
atlas/support/tier1/    → [anthropic.com, zendesk.com] (inherited)
```

Resolution: walk folder path, collect all rules, dedupe.

## Implementation

| Step | What                                               |
| ---- | -------------------------------------------------- |
| 1    | Add crackbox as Go module dependency               |
| 2    | Initialize `Isolation` in gated/main.go            |
| 3    | Replace docker exec in container/runner.go         |
| 4    | Add network_rules table + migration                |
| 5    | Add MCP tools: request_network, list_network_rules |
| 6    | Wire allowlist resolution (folder walk)            |
| 7    | Test: message → gated → crackbox → ant → response  |

## Proxy location

Proxy runs in gated process (simplest — gated already has docker access).

## Out of scope

- QEMU backend for arizuko (Docker is fine)
- Per-user network rules (per-group only)
- Approval workflow UI (CLI/MCP only)
- Traffic logging/auditing (future)
