---
status: draft
---

# Crackbox — minimal isolated sandbox for Claude

> Simpler than Docker. Network-isolated execution for Claude Code.
> QEMU VMs or Docker containers, same interface.

## Problem

Running Claude Code safely requires network isolation. Docker is complex
(daemon, images, compose, networks, volumes). Users want:

```bash
crackbox run .    # just works, isolated, claude-code inside
```

## Solution

Single binary, ~900 LOC. Two backends (QEMU, Docker), one interface.
Default-deny networking with per-sandbox domain allowlist.

## CLI

```bash
# Run isolated sandbox
crackbox run .                          # current dir, attach
crackbox run ~/project                  # specific path
crackbox run -d .                       # detached, returns ID
crackbox run --allow github.com .       # with initial allowlist

# Manage
crackbox ls                             # list running sandboxes
crackbox stop <id>                      # stop sandbox
crackbox rm <id>                        # remove sandbox

# Network rules (while running)
crackbox allow <id> github.com          # add domain
crackbox allow <id> 8.8.8.8             # add IP
crackbox allow <id> --all               # full internet
crackbox deny <id> github.com           # remove rule
crackbox list <id>                      # show allowlist

# Connect
crackbox ssh <id>                       # SSH into sandbox
crackbox exec <id> <cmd>                # run command
```

## Backends

```bash
crackbox run .                  # Docker (default, faster)
crackbox run --vm .             # QEMU VM (stronger isolation)
CRACKBOX_BACKEND=qemu crackbox run .  # env override
```

| Backend | Start  | Isolation    | Use case                        |
| ------- | ------ | ------------ | ------------------------------- |
| Docker  | 1-3s   | Network only | Default, fast iteration         |
| QEMU    | 10-30s | Full VM      | Untrusted code, security audits |

Same CLI, same network isolation. Backend is implementation detail.

## Network isolation

**Default: no internet.** All traffic blocked except:

1. **Proxy** at 10.99.0.1:3128 — domain filtering
2. **DNS** at 10.99.0.1:53 — resolved by host

Sandbox gets `http_proxy` env. Well-behaved tools (curl, npm, pip, git)
route through proxy automatically. Proxy checks allowlist per-sandbox.

Raw IP traffic (not HTTP) blocked by iptables unless explicitly allowed.

```
┌──────────────────────────────────────────────────────────┐
│  Sandbox (10.99.x.y)                                     │
│                                                          │
│  HTTP/HTTPS ──► Proxy (10.99.0.1:3128) ──► allowlist     │
│                         │                                │
│                    ┌────▼────┐                           │
│                    │ github  │ ✓ allowed                 │
│                    │ evil.io │ ✗ blocked                 │
│                    └─────────┘                           │
│                                                          │
│  Raw TCP/UDP ──► iptables ──► DROP (unless allow <ip>)   │
└──────────────────────────────────────────────────────────┘
```

## Zero config

```bash
# Install
curl -L https://... | sudo sh

# Use
crackbox run .
```

No daemon. No config files. No images to pull (base baked in or
downloaded once). Just works.

## Data directory

```
~/.local/share/crackbox/          # user mode (default)
/var/lib/crackbox/                # system mode

├── base/
│   ├── alpine.qcow2              # QEMU base image (downloaded once)
│   └── Dockerfile                # Docker base (built once)
└── sandboxes/
    └── <id>/
        ├── meta.json             # state, allowlist
        ├── overlay.qcow2         # QEMU overlay (if VM)
        └── workspace/            # mounted into sandbox
```

## What's inside

Alpine Linux with Claude Code pre-installed:

- Languages: Go, Node.js, Python 3.14 (uv), Bun, Rust
- Tools: git, vim, ripgrep, fd, jq, curl
- claude-code CLI ready to use

## Architecture

```go
// ~900 LOC total
crackbox/
├── crackbox.go      // Isolation type, Run/Stop/Allow/Deny
├── proxy.go         // HTTP proxy, per-sandbox allowlist (~230 LOC)
├── netfilter.go     // iptables rules (~150 LOC)
├── backend.go       // Backend interface
├── docker.go        // DockerBackend (~80 LOC)
├── qemu.go          // QEMUBackend (~200 LOC)
└── cmd/crackbox/
    └── main.go      // CLI (~200 LOC)
```

## Implementation

### Phase 1: Port from existing crackbox

1. Copy `internal/vm/proxy.go` → `crackbox/proxy.go`
2. Copy `internal/vm/netfilter.go` → `crackbox/netfilter.go`
3. Abstract VM-specific code into Backend interface
4. Add DockerBackend

### Phase 2: Simplify CLI

1. Single `crackbox` binary (no daemon)
2. Remove web dashboard, payments, Telegram bot
3. Remove multi-user features
4. Keep: run, ls, stop, rm, allow, deny, ssh, exec

### Phase 3: Polish

1. Auto-download base image on first run
2. XDG-compliant data directory
3. Man page, shell completions
4. Package for Homebrew, apt, etc.

## Comparison

```
Feature              Docker          Crackbox
─────────────────────────────────────────────
Install              complex         curl | sh
Config               daemon, compose none
Images               registries      one base, baked
Network isolation    opt-in          default
CLI surface          100+ commands   10 commands
Learning curve       days            minutes
```

## Non-goals

- General container runtime (use Docker/Podman)
- Orchestration (use K8s)
- Image management (one base image)
- Multi-user (single user tool)
- Production deployment (dev/sandbox only)

Crackbox is **not** Docker. It's a Claude sandbox with network isolation.
