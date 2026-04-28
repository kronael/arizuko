---
status: draft
depends: 9-crackbox-standalone
---

# Crackbox integration into arizuko

> Use crackbox library for network-isolated agent execution in arizuko.
> Depends on spec 43 (standalone crackbox).

## Problem

arizuko's `container/runner.go` spawns Docker containers with unrestricted
network access. MCP tools can exfiltrate data or call unauthorized APIs.

## Solution

Import crackbox as a Go library. Replace direct `docker run` with
`crackbox.Run()`. Network isolation comes for free.

## Changes to gated

### Before (current)

```go
// container/runner.go
cmd := exec.Command("docker", "run", "-i", "--rm", ...)
cmd.Start()
```

### After

```go
// gated/main.go — at startup
iso := crackbox.New(crackbox.Config{
    Backend:     crackbox.DockerBackend{},
    ProxyAddr:   "10.99.0.1:3128",
    FilterChain: "ARIZUKO_FILTER",
    Subnet:      "10.99.0.0/16",
})

// container/runner.go — when spawning
id, err := iso.Run(crackbox.RunOpts{
    Name:      containerName,
    Image:     cfg.Image,
    Mounts:    mounts,
    Env:       secrets,
    Allowlist: allowlistForGroup(in.Folder),
})
```

## Allowlist source

Per-group network rules. Options:

### Option A: Reuse secrets table

```sql
-- scope_kind="network", key is the target
INSERT INTO secrets (scope_kind, scope_id, key, enc_value)
VALUES ('network', 'atlas/support', 'github.com', '');
```

Pros: No new table. Cons: Awkward fit (no encryption needed).

### Option B: New network_rules table

```sql
CREATE TABLE network_rules (
    folder     TEXT NOT NULL,
    target     TEXT NOT NULL,  -- domain or IP/CIDR
    created_at DATETIME NOT NULL,
    PRIMARY KEY (folder, target)
);
```

Pros: Clean. Cons: Another table.

### Option C: Group metadata YAML

```yaml
# groups/atlas/support/.meta.yml
allowlist:
  - anthropic.com
  - api.anthropic.com
  - github.com
```

Pros: Simple, file-based. Cons: Not in DB, harder to query.

**Recommendation: Option B** (new table). Clean, queryable, consistent
with secrets pattern.

## MCP tools

Add tools for agents to request network access:

```go
// mcp/network.go
func (m *MCP) RequestNetworkAccess(target string) error {
    // Log request
    // Optionally: auto-approve for tier 0-1, require approval for others
    // Add to allowlist via crackbox.Allow()
}

func (m *MCP) ListNetworkRules() []string {
    return m.iso.Allowlist(m.containerID)
}
```

Tier-based auto-approval:

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

## Compose changes

crackbox proxy needs to run on host network to be reachable from
containers. Either:

1. Run proxy in gated process (simplest)
2. Add proxy container with `network_mode: host`

Option 1 is simpler — gated already runs with docker access.

## Rollout

1. **Opt-in**: `ARIZUKO_ISOLATION=crackbox` env var
2. **Default off**: existing behavior unchanged
3. **Gradual**: enable per-instance (krons first)
4. **Default on**: after validation

## File changes

```
gated/main.go           # Initialize crackbox.Isolation
container/runner.go     # Use iso.Run() instead of docker exec
store/network.go        # network_rules CRUD
store/migrations/0035   # CREATE TABLE network_rules
ipc/mcp_network.go      # MCP tools for network access
```

## Not in this spec

- QEMU backend for arizuko (Docker is fine)
- Per-user network rules (per-group only)
- Approval workflow UI (CLI/MCP only)
- Traffic logging/auditing (future)
