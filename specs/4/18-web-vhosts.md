## status: shipped

# Web Virtual Hosts

Hostname-based routing via proxyd. Root agent manages mappings.
Worlds write content to their subdirectory.

## Problem

A single arizuko instance hosts multiple worlds. Each world needs
its own hostname (`REDACTED`, `atlas.REDACTED`) without
per-world configuration in the gateway.

## Design

### Hostname → redirect

`proxyd` reads `vhosts.json`, matches `Host` header,
and issues a `301` redirect to the world's subdirectory.
Vite serves the subdirectory normally.

```
GET / Host: REDACTED
→ 301 Location: /REDACTED/
→ Vite serves DATA_DIR/web/REDACTED/index.html
```

### vhosts.json

```json
{
  "REDACTED": "REDACTED",
  "*.atlas.REDACTED": "atlas"
}
```

Lives at `DATA_DIR/web/vhosts.json`. Hostname keys support glob
patterns (`*` prefix wildcard). Root agent (tier 0) writes it
directly — no gateway code, no DB table, no IPC actions.

Matching: exact first, then glob via `path.Match`. Port stripped
before matching.

### Path safety

Two layers prevent cross-world serving:

1. **Mount isolation** — tier 1 container bind-mounts
   `DATA_DIR/web/<world>/` as `/workspace/web/`. Cannot write
   outside own directory.

2. **Redirect validation** — proxyd rejects `..` in the raw URL
   (400), then normalizes with `path.Clean` before redirecting.

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

Baked into agent image for tier 0 only.

## World workflow

A tier 1 world agent writes web content:

```
/workspace/web/index.html     ← its own web root
/workspace/web/assets/
```

Inside the container, `/workspace/web/` is bind-mounted to
`DATA_DIR/web/<world>/`. The world doesn't know about hostnames —
it just writes files. The redirect serves them at `{world}.{domain}`.

## Implementation notes

- `proxyd/main.go`: `vhosts.load()` checks mtime every 5s, swaps
  atomically under `sync.RWMutex`. Redirect runs before auth check.
- `core.Config.WebDir` = `DATA_DIR/web`; vhosts path =
  `filepath.Join(cfg.WebDir, "vhosts.json")`

## Related

- `specs/3/5-permissions.md` — tier model, mount table
