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

Mimics Docker. Default persists (no --rm), explicit rm to remove.

```bash
# Run isolated sandbox
crackbox run .                          # create + start + attach, persists
crackbox run ~/project                  # specific path
crackbox run -d .                       # detached, returns ID
crackbox run --rm .                     # ephemeral, removes on exit
crackbox run --allow github.com .       # with initial allowlist
crackbox run --timeout 30m .            # auto-stop after 30min idle

# Lifecycle (like docker)
crackbox start <id>                     # start stopped sandbox
crackbox stop <id>                      # stop running sandbox
crackbox rm <id>                        # remove stopped sandbox
crackbox rm -f <id>                     # force remove running

# List
crackbox ls                             # list running
crackbox ls -a                          # list all (including stopped)

# Network rules (while running)
crackbox allow <id> github.com          # add domain
crackbox allow <id> 8.8.8.8             # add IP
crackbox allow <id> --all               # full internet
crackbox deny <id> github.com           # remove rule
crackbox rules <id>                     # show allowlist

# Connect / Execute
crackbox exec <id> bash                 # run command (like docker exec)
crackbox exec -it <id> bash             # interactive (default if tty)
crackbox ssh <id>                       # SSH into sandbox (QEMU only)
crackbox attach <id>                    # attach to running sandbox
```

## States

Same as Docker:

```
        run
         │
         ▼
     ┌───────┐    stop    ┌─────────┐    rm     ┌─────────┐
     │running│ ─────────► │ stopped │ ────────► │ removed │
     └───────┘            └─────────┘           └─────────┘
         ▲                     │
         │        start        │
         └─────────────────────┘
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

## Base image

Alpine Linux + Claude Code + standard dev tools (Go, Node, Python, Rust,
git, ripgrep). Built once, downloaded on first run.

## Out of scope

General container runtime, orchestration, multi-user, production deployment.
