---
status: draft
---

# Crackbox integration into arizuko

> Integrate crackbox as an arizuko component for isolated agent execution.
> Works standalone (claude-code default) or with arizuko (ant with hookings).
> Dockbox variant: Docker containers instead of QEMU VMs.

## Problem

arizuko containers have unrestricted network access. MCP tools can exfiltrate
data or make unauthorized API calls. No network isolation layer exists.

## Solution

Port crackbox's isolation model into arizuko:

1. **crackbox** (existing) — QEMU/KVM VMs with per-VM network filtering
2. **dockbox** (new) — Docker containers with same isolation model
3. **ant** — the workload image (claude-code + tools + arizuko hookings)

## Architecture

```
┌────────────────────────────────────────────────────────────────────┐
│                           HOST                                     │
│  ┌──────────────────┐   ┌──────────────────┐   ┌───────────────┐   │
│  │  arizuko (gated) │◄─►│  crackboxd/      │◄─►│  proxy        │   │
│  │                  │   │  dockboxd        │   │  (10.99.0.1)  │   │
│  └──────────────────┘   └──────────────────┘   └───────┬───────┘   │
│                                                        │           │
│  ┌─────────────────────────────────────────────────────▼────────┐  │
│  │                    Isolated Network                          │  │
│  │  ┌──────────────┐  ┌──────────────┐  ┌──────────────┐        │  │
│  │  │  ant (VM)    │  │  ant (VM)    │  │  ant (dock)  │        │  │
│  │  │  group: foo  │  │  group: bar  │  │  group: baz  │        │  │
│  │  │  10.99.1.x   │  │  10.99.2.x   │  │  10.99.3.x   │        │  │
│  │  └──────────────┘  └──────────────┘  └──────────────┘        │  │
│  └──────────────────────────────────────────────────────────────┘  │
└────────────────────────────────────────────────────────────────────┘
```

## Modes

### Standalone (crackbox/dockbox)

Default workload: `claude-code`. User manages VMs/containers directly.

```bash
crackbox create my-sandbox
crackbox allow my-sandbox github.com
crackbox ssh my-sandbox
# claude-code runs inside

dockbox create my-sandbox
dockbox allow my-sandbox github.com
dockbox exec my-sandbox
# claude-code runs inside
```

### With arizuko

Workload: `ant` (arizuko-ant image). gated orchestrates via crackboxd/dockboxd API.

```
gated spawns container
  ├─ calls crackboxd/dockboxd API: create(group=foo, image=ant)
  ├─ sets network rules: allow(id, anthropic.com, github.com, ...)
  ├─ mounts IPC socket via 9p (VM) or volume (Docker)
  └─ sends Input JSON with secrets to ant
```

## Dockbox implementation

Port crackbox's isolation model to Docker:

### Network isolation

```go
// Create isolated network with proxy gateway
docker network create --subnet=10.99.0.0/16 --gateway=10.99.0.1 dockbox-net

// Container gets http_proxy pointing to gateway
docker run --network=dockbox-net \
  -e http_proxy=http://10.99.0.1:3128 \
  -e https_proxy=http://10.99.0.1:3128 \
  arizuko-ant:latest
```

### Proxy server (reuse crackbox's proxy.go)

```go
// Same per-container domain filtering
// Source IP → container lookup → check allowlist → forward or 403
type ProxyServer struct {
  manager  ContainerManager  // interface: VM or Docker
  listener net.Listener
}
```

### iptables rules

```bash
# Default deny for dockbox-net
iptables -I FORWARD -s 10.99.0.0/16 -j DOCKBOX_FILTER
iptables -I FORWARD -d 10.99.0.0/16 -j DOCKBOX_FILTER

# DOCKBOX_FILTER chain
iptables -A DOCKBOX_FILTER -m state --state ESTABLISHED,RELATED -j ACCEPT
iptables -A DOCKBOX_FILTER -d 10.99.0.1 -j ACCEPT  # proxy + DNS
iptables -A DOCKBOX_FILTER -s 10.99.0.1 -j ACCEPT
# [per-container allow rules inserted here]
iptables -A DOCKBOX_FILTER -j DROP  # default deny
```

### API (same as crackbox)

```
POST   /vm           create(name, image, auto_start)
GET    /vm           list()
GET    /vm/{id}      get(id)
POST   /vm/{id}/start
POST   /vm/{id}/stop
DELETE /vm/{id}
POST   /vm/{id}/allow    allow(id, target)  # domain or IP
POST   /vm/{id}/deny     deny(id, target)
GET    /vm/{id}/allowlist
```

## ant image changes

Currently ant expects direct internet access. With crackbox/dockbox:

1. **http_proxy env** — already respected by most tools (curl, npm, pip, etc.)
2. **IPC socket** — mounted at `/workspace/ipc/gated.sock` (same as today)
3. **Secrets** — passed via Input JSON (same as today)

No ant code changes needed for basic operation. The proxy is transparent.

## gated integration

Replace `container/runner.go` Docker spawning with crackboxd/dockboxd API calls:

```go
// Current: direct docker run
cmd := exec.Command("docker", "run", ...)

// New: via dockboxd API
resp, _ := http.Post("http://localhost:49160/vm", "application/json",
  bytes.NewReader(json.Marshal(CreateRequest{
    Name:  containerName,
    Image: cfg.Image,
    Mounts: mounts,
    Env:   envVars,
  })))
```

Network rules come from group metadata (new table or reuse secrets):

```sql
CREATE TABLE network_rules (
  folder     TEXT NOT NULL,
  target     TEXT NOT NULL,  -- domain or IP/CIDR
  created_at DATETIME NOT NULL,
  PRIMARY KEY (folder, target)
);
```

gated sends allowlist to crackboxd/dockboxd after container creation.

## CLI unification

Both crackbox and dockbox share the same CLI interface:

```bash
# QEMU VM (stronger isolation, slower)
crackbox create my-sandbox

# Docker container (lighter, faster)
dockbox create my-sandbox

# Same commands for both
crackbox allow my-sandbox github.com
dockbox allow my-sandbox github.com
```

## Implementation phases

| Phase | What                                                         | Lift   |
| ----- | ------------------------------------------------------------ | ------ |
| 1     | Copy crackbox into arizuko repo as `crackbox/`               | 1 day  |
| 2     | Extract common interfaces (ContainerManager, ProxyServer)    | 2 days |
| 3     | Implement dockbox backend using extracted interfaces         | 3 days |
| 4     | Integrate dockboxd API into gated's container runner         | 2 days |
| 5     | Add network_rules table and MCP tools                        | 1 day  |
| 6     | Test end-to-end: message → gated → dockboxd → ant → response | 2 days |

## Open questions

1. **Where does crackboxd/dockboxd run?**
   - Same compose as arizuko? Separate systemd service?
   - Needs root/CAP_NET_ADMIN for iptables

2. **VM vs Docker default for arizuko?**
   - Docker (dockbox) is lighter, probably default
   - VM (crackbox) for higher security requirements

3. **Per-group vs per-user network rules?**
   - Start with per-group (simpler, matches secrets model)
   - Per-user as future extension

4. **Allowlist inheritance?**
   - Child groups inherit parent's allowlist?
   - Or explicit per-group only?

## References

- `/home/onvos/app/crackbox/` — existing crackbox implementation
- `crackbox/internal/vm/proxy.go` — per-VM proxy filtering (~230 LOC)
- `crackbox/internal/vm/netfilter.go` — iptables rule management
- `specs/7/35-tenant-self-service.md` — secrets model (similar pattern)
