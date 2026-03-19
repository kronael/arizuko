---
name: infra
description: Instance-level infrastructure management. Tier 0 (root) only.
user-invocable: true
---

# Infra Skill

Instance-level infrastructure management. Tier 0 (root) only.

## Hostname Assignment

Map a hostname to a world's web directory:

1. Read current `/workspace/web/vhosts.json` (create `{}` if missing)
2. Add entry: `{"hostname.example.com": "worldname"}`
3. Write back to `/workspace/web/vhosts.json`
4. Verify DNS: `dig +short hostname.example.com`
5. Create web dir if needed: `mkdir -p /workspace/web/worldname/`

The gateway reloads vhosts.json automatically (5s mtime check).

## DNS Verification

Before assigning a hostname, verify it resolves:

```bash
dig +short hostname.example.com
```

Must return the instance's public IP. If not, the hostname
won't work until DNS propagates.

## SSL/TLS

TLS termination is handled by the reverse proxy (Caddy/nginx)
in front of kanipi, not by kanipi itself. Caddy auto-provisions
Let's Encrypt certs for configured domains.

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
via 301 redirect from web-proxy.
