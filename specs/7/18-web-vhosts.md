## <!-- source: arizuko specs/3/8-web-virtual-hosts.md, synced 2026-03-15 -->

## status: shipped

# Web Virtual Hosts

Hostname-based routing via web-proxy.ts redirect. Root agent
manages mappings. Worlds write content to their subdirectory.

## Problem

A single arizuko instance hosts multiple worlds. Each world needs
its own hostname (`REDACTED`, `atlas.REDACTED`) without
per-world configuration in the gateway.

## Design

### Hostname → redirect

`web-proxy.ts` reads `vhosts.json`, matches `Host` header,
and issues a `301` redirect to the world's subdirectory.
Vite serves the subdirectory normally — no plugins, no
`server.fs.allow` changes, no hidden rewrites.

```
GET / Host: REDACTED
→ 301 Location: /REDACTED/
→ Vite serves DATA_DIR/web/REDACTED/index.html
```

### vhosts.json

```json
{
  "REDACTED": "REDACTED",
  "support.acme.co": "atlas"
}
```

Lives at `DATA_DIR/web/vhosts.json`. Root agent (tier 0) writes
it directly — no gateway code, no DB table, no IPC actions.

### Path safety

Two layers prevent cross-world serving:

1. **Mount isolation** — tier 1 container bind-mounts
   `DATA_DIR/web/<world>/` as `/workspace/web/`. Cannot write
   outside own directory. Symlinks resolve inside the mount.

2. **Redirect validation** — web-proxy rejects traversal in
   the raw URL, then normalizes before redirecting:

```typescript
if (url.includes('..')) {
  res.writeHead(400).end();
  return;
}
const normalized = path.posix.normalize(url);
res.writeHead(301, { Location: `/${world}${normalized}` });
```

`vhosts.json` is only writable by root (tier 0), so worlds
cannot redirect to each other's namespaces.

### Mount changes

| Mount                     | Tier 0 | Tier 1              | Tier 2+ |
| ------------------------- | ------ | ------------------- | ------- |
| `/workspace/web`          | rw     | no                  | no      |
| `/workspace/web/<world>/` | —      | rw (own world only) | no      |

Tier 1 sees `/workspace/web/` as its own web root. Implemented
as a bind mount of `DATA_DIR/web/<world>/` → `/workspace/web/`.

### Root infra skill

Root agent gets an `infra` skill (`~/.claude/skills/infra/`)
for instance-level setup:

- Hostname assignment (write to `vhosts.json`)
- DNS verification (resolve check)
- SSL/TLS notes
- Web directory structure

Baked into agent image for tier 0 only. Worlds don't see it.

## World workflow

A tier 1 world agent writes web content:

```
/workspace/web/index.html     ← its own web root
/workspace/web/assets/         ← static files
```

Inside the container, `/workspace/web/` is bind-mounted to
`DATA_DIR/web/<world>/`. The world doesn't know about hostnames —
it just writes files. The redirect serves them at `{world}.{domain}`.

## Implementation order

1. ~~web-proxy.ts: read `vhosts.json`, redirect by `Host` header~~ **shipped**
2. ~~Change tier 1 mount: `/workspace/web/` → `DATA_DIR/web/<world>/`~~ **shipped**
3. ~~`infra` skill for root agent~~ **shipped**

## Implementation notes

- `loadVhosts()` in `web-proxy.ts` caches `vhosts.json`, re-checks
  file mtime every 5s. Redirect runs before auth check.
- `HOST_WEB_DIR` added to `config.ts` for host-path mount resolution.
- `container-runner.ts` `buildVolumeMounts()`: tier 0 mounts full `WEB_DIR`;
  tier 1 mounts `WEB_DIR/<world>/` as `/workspace/web` (per-world isolation).
- Tests in `web-proxy.test.ts`: vhost redirect, path traversal
  rejection, no-match fallthrough, missing vhosts.json.

## Related

- `specs/3/5-permissions.md` — tier model, mount table
