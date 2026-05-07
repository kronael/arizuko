---
name: infra
description: Root-group only. Assign virtual hostnames, manage vhosts.json, verify DNS.
when_to_use: Use when adding a new domain or hostname to an instance.
user-invocable: true
---

# Infra

Root-only. Map hostnames to world web directories.

## Steps

1. Read `/workspace/web/vhosts.json` (create `{}` if missing)
2. Add `{"hostname.example.com": "worldname"}`
3. Write back
4. `dig +short hostname.example.com` to verify DNS
5. `mkdir -p /workspace/web/worldname/` if needed

Gateway reloads `vhosts.json` automatically (5s mtime check). TLS is handled
by the reverse proxy (Caddy + Let's Encrypt).
