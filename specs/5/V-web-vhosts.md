---
status: shipped
---

# Web Virtual Hosts + Agent Web Slots

Hostname-based routing via proxyd + per-group writable web slots in
the agent's home. Root agent manages vhost mappings; every group
writes to its own slot via Apache-mod_userdir-style paths.

## Problem

A single arizuko instance hosts multiple worlds. Each world needs its
own hostname (`REDACTED`, `atlas.REDACTED`) without per-world
configuration in the gateway. Each group also needs a place to
publish web content — public for everyone, private behind OAuth —
without growing platform mechanism per-group.

## Design

### Hostname → redirect

`proxyd` reads `vhosts.json`, matches `Host` header, and issues a
`301` redirect to the world's subdirectory. Vite serves the
subdirectory normally.

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

1. **Mount isolation** — each group's container bind-mounts only its
   own slot into `~/public_html/` and `~/private_html/`. The unified
   `<data>/web/{pub,priv}/` trees live on the host; vited/webd serve
   from there directly.

2. **Redirect validation** — proxyd rejects `..` in the raw URL
   (400), then normalizes with `path.Clean` before redirecting.

`vhosts.json` is only writable by root (tier 0), so worlds cannot
redirect to each other's namespaces.

## Agent web slots (v0.45.11+)

Every group container has two writable web slots in its home, plus a
read-only view of the unified public web tree.

### The slots

| Container path    | URL served at         | Auth      | Bind-mount source            |
| ----------------- | --------------------- | --------- | ---------------------------- |
| `~/public_html/`  | `/pub/<folder>/...`   | none      | `<data>/web/pub/<folder>/`   |
| `~/private_html/` | `/priv/<folder>/...`  | OAuth/JWT | `<data>/web/priv/<folder>/`  |
| `/var/lib/www/`   | (read access, no URL) | n/a       | `<data>/web/pub/` (RO whole) |

### Why bind-mount, not symlink

The unified web tree at `<data>/web/{pub,priv}/` is canonical
filesystem. vited/webd serve from there directly. Each agent
container gets a bind-mount VIEW of its own subdir into
`~/public_html` / `~/private_html`. No symlinks; the same bytes
appear at two paths because the kernel binds them.

### Nested subgroup URLs

A tier-2 group `atlas/support` has its `~/public_html` bind-mounted
from `<data>/web/pub/atlas/support/` — a real subpath of atlas's own
pub tree. Hierarchy preserved in URLs (`/pub/atlas/support/...`).

When atlas's own pub tree has a subdir named `support` that doesn't
belong to a subgroup, atlas writes freely. When `atlas/support`
subgroup exists, the bind mount for atlas/support's container takes
precedence INSIDE its own container. From atlas's container, the
subgroup's content shows up as RO via `/var/lib/www/atlas/support/`
— atlas sees it, knows not to overwrite.

### URL → filesystem mapping

| URL                       | Filesystem (on host)   | Auth                       |
| ------------------------- | ---------------------- | -------------------------- |
| `https://<host>/pub/<X>`  | `<data>/web/pub/<X>`   | none                       |
| `https://<host>/priv/<X>` | `<data>/web/priv/<X>`  | JWT (proxyd-gated)         |
| `https://<host>/<X>`      | rewrites to `/pub/<X>` | JWT (existing fallthrough) |

The `/pub/<X>` and `/<X>` URLs serve the SAME file — different doors.
The `/priv/<X>` URL serves a DIFFERENT filesystem tree; content
under `web/priv/` is NEVER served via `/pub/` URLs.

### Truly private (off-web) storage

`~/workspace/`, `~/diary/`, `~/facts/`, `~/users/`, `~/.claude/`,
and other `~/*` subdirs are NOT bind-mounted into any web tree.
Files there have no URL and are accessible only inside the container
(group home is mounted from `<data>/groups/<folder>/`).

## Platform mount paths (v0.45.11+)

Platform mounts moved to FHS canonical locations. The previous
`/workspace/*` prefix was a devcontainer convention misapplied.

| Container         | Host                           | Mode                    |
| ----------------- | ------------------------------ | ----------------------- |
| `/opt/arizuko`    | `<repo>`                       | RO                      |
| `/var/lib/www`    | `<data>/web/pub/`              | RO whole tree, tier 0-2 |
| `/run/ipc`        | `<data>/ipc/<folder>/`         | RW                      |
| `/var/lib/share`  | `<data>/groups/<world>/share/` | RO/RW per grant         |
| `/var/lib/groups` | `<data>/groups/`               | RW, tier 0 only         |
| `/mnt/<name>`     | operator extras                | varies                  |
| `/home/node/`     | `<data>/groups/<folder>/`      | RW (group home)         |

### Mount permissions per tier

| Mount             | Tier 0 | Tier 1            | Tier 2+           |
| ----------------- | ------ | ----------------- | ----------------- |
| `/var/lib/www`    | rw     | no (RO view only) | no (RO view only) |
| `~/public_html/`  | rw     | rw                | rw                |
| `~/private_html/` | rw     | rw                | rw                |

Tier 0 directly owns `<data>/web/pub/` and can stage content for any
group via `/var/lib/www` rw. Tier 1+ writes through its own
`~/public_html/` / `~/private_html/` slot, which the bind mount
projects into the unified tree.

### Root infra skill

Root agent gets an `infra` skill (`~/.claude/skills/infra/`) for
instance-level setup:

- Hostname assignment (write to `vhosts.json`)
- DNS verification (resolve check)
- SSL/TLS notes
- Web directory structure

Baked into agent image for tier 0 only.

## World workflow

A tier-1 world agent writes web content:

```
~/public_html/index.html      → /pub/<folder>/
~/public_html/assets/         → /pub/<folder>/assets/
~/private_html/admin.html     → /priv/<folder>/admin.html (OAuth)
```

The bind mount projects each path into the unified
`<data>/web/{pub,priv}/<folder>/` tree. The world doesn't know about
hostnames — it just writes files into its home. The vhost redirect
serves `~/public_html/` at `{world}.{domain}/`.

## Implementation notes

- `proxyd/main.go`: `vhosts.load()` checks mtime every 5s, swaps
  atomically under `sync.RWMutex`. Redirect runs before auth check.
- `core.Config.WebDir` = `DATA_DIR/web`; vhosts path =
  `filepath.Join(cfg.WebDir, "vhosts.json")`.
- `container/runner.go` adds two bind mounts per group:
  `<data>/web/pub/<folder>/` → `~/public_html` and
  `<data>/web/priv/<folder>/` → `~/private_html`. Both are created
  via `MkdirAll` at spawn time.
- `vited` mounts `DATA_DIR/web:/web` and serves from `/web/`
  (WEB_ROOT default). Writes through `~/public_html/index.html` land
  in `<data>/web/pub/<folder>/index.html` → vite serves at
  `/pub/<folder>/`.
- `/priv/*` routing requires proxyd to JWT-gate the prefix and serve
  from `<data>/web/priv/`; see `specs/4/2-proxyd.md`.

## Related

- `specs/3/5-tool-authorization.md` — tier model, mount table
- `specs/3/8-web-virtual-hosts.md` — older vhost spec, superseded
  here for the web-slot model
- `specs/4/2-proxyd.md` — `/pub/*` and `/priv/*` route handling
