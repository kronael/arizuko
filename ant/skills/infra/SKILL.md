---
name: infra
description: >
  Root-group only. Assign virtual hostnames, manage vhosts.json,
  verify DNS. Use when adding a new domain or hostname to an instance.
user-invocable: true
---

# Infra

Root-only. Manage virtual hostnames and web directory structure.

## Hostname Assignment

Map a hostname to a world's web directory:

1. Read current `/workspace/web/vhosts.json` (create `{}` if missing)
2. Add entry: `{"hostname.example.com": "worldname"}`
3. Write back to `/workspace/web/vhosts.json`
4. Verify DNS: `dig +short hostname.example.com`
5. Create web dir if needed: `mkdir -p /workspace/web/worldname/`

The gateway reloads vhosts.json automatically (5s mtime check).

## DNS Verification

```bash
dig +short hostname.example.com
```

## SSL/TLS

TLS terminated by reverse proxy (Caddy) with auto Let's Encrypt.

## Web Directory Structure

```
/workspace/web/
  vhosts.json          <- hostname -> world mapping
  REDACTED/               <- world web root
    index.html
  atlas/
    index.html
```

Each world's web content is served at `https://hostname/`
via internal path rewrite by proxyd.
