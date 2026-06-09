---
status: partial
---

# Web Virtual Hosts + Agent Web Slots

Per-group writable web slots in the agent's home, served through
proxyd. Each world is reachable at a **derived** hostname that
redirects to its public slot — no per-host config, no vhost file, no
DB table. (The agent web-slot + mount model is shipped; the derived
host-redirect supersedes the retired `vhosts.json` mechanism and is
the `partial` part.)

## Problem

A single arizuko instance hosts multiple worlds. Each world needs its
own hostname (`krons.fiu.wtf`, `atlas.fiu.wtf`) without per-world
configuration in the proxy. Each group also needs a place to publish
web content — public for everyone, private behind OAuth — without
growing platform mechanism per-group. The original `vhosts.json` map
was the only web-routing mechanism that was neither derived nor
DB-backed: a hand-edited file, out of step with the rest of the
routing surface, and drifted (spec said `301`, code did an in-place
rewrite). It is retired.

## Design

### Hostname → world (derived redirect)

A world's vhost is **derived**, never configured per host. The
deployment sets one `HOSTING_DOMAIN` (krons: `fiu.wtf`); world `W` is
reached at `W.<HOSTING_DOMAIN>`. proxyd recovers `W` from the `Host`
header by _boundary-checked_ suffix removal — `host == W + "." +
HOSTING_DOMAIN` after lower-casing, port-stripping, and trimming a
trailing dot, NOT a raw `HasSuffix` (so `notkrons.fiu.wtf` never maps
to `krons`). `W` must be a single label (no dot — worlds are
single-label folders) and a real world; anything else is treated as
un-mapped and falls through to normal routing. Bare `HOSTING_DOMAIN`
(no subdomain → `W==""`) is un-mapped — never `/pub//`.

On a mapped host, proxyd issues a same-origin `302` to the world's
canonical public slot, preserving sub-path and query:

```
GET /                    Host: krons.fiu.wtf  → 302 /pub/krons/
GET /biotech-swe-guide/  Host: krons.fiu.wtf  → 302 /pub/krons/biotech-swe-guide/
GET /x?a=1               Host: krons.fiu.wtf  → 302 /pub/krons/x?a=1
```

The mapping is the deterministic composition `W.<HOSTING_DOMAIN>` and
its reverse — no file, no row. This keeps "identity is configured,
never derived" (CLAUDE.md) honest: the world _name_ (its folder) and
`HOSTING_DOMAIN` are both configured; the host is only their
composition. The redirect `Location` is always a relative `/pub/<W>/…`
path, never an absolute URL, so an attacker-supplied `Host` cannot
open-redirect.

### Where the redirect runs — and why it can't loop

The world-redirect replaces ONLY the final public catch-all
(`proxyd/main.go:612`, today `http.Redirect(…, "/pub"+path)`). Every
reserved surface is dispatched BEFORE that point, so it is served in
place on every vhost (see below). The 302 lands on `/pub/W/…`; the
follow-up request — same host — enters the `/pub/` branch and stops. A
reserved prefix never re-enters the catch-all, so the redirect cannot
loop. The `..` / `%2e%2e` / `%2f` rejection that guarded the old
in-place rewrite moves onto the redirect builder. Running the
derivation anywhere earlier than the catch-all would both reintroduce
loop risk and shadow the reserved surfaces — so it lives only there.

`vhosts.json`, the `vhosts` struct, its mtime-reload loop, and the
in-place Host-match rewrite (`proxyd/main.go` ~118-175, ~494-517) are
**retired**.

### Reserved prefixes are global

`/auth /dash /pub /priv /dav /chat /hook /me /x /api /health` and
every `[[proxyd_route]]` backend prefix are served identically on
**every** vhost, because they are matched ahead of the catch-all. So
`krons.fiu.wtf/dash/`, `/auth/login`, `/pub/other/` all work from any
world's hostname — no separate dashboard/system domain. A world's
hostname is just a friendly front door to its `/pub/<W>/` slot; the
platform surfaces are everywhere.

### Layering — rewrites compose on top

The derived host-redirect is the OUTER layer; the existing DB-backed
redirects ("One URL, one backing store", below) are INNER and
unchanged:

1. `W.<HOSTING_DOMAIN>/<path>` → 302 → `/pub/W/<path>` (derived).
2. `/pub/W/<path>` → longest-prefix `web_routes` match
   (`redirect`/`deny`/`auth`), else vited serves the file (the
   agent-managed aliases via `set_web_route`).
3. vited serves `<data>/web/pub/W/<path>`, resolving `index.html` for
   a trailing-slash directory request (`ant/vite.config.js`
   `serveIndex`).

A world only writes into `~/public_html/`; its hostname and the
redirect are platform-derived — it never edits a host map. The one
residual loop risk is a `web_routes` row that redirects back to a
non-reserved same-host path; that is the agent's own misconfiguration,
not the platform's, and is bounded to its own slot by `set_web_route`.

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

- `HOSTING_DOMAIN` (set once in the instance `.env`) — per-world
  hostnames are derived from it, so there is no per-host assignment step
- DNS: a wildcard `*.<HOSTING_DOMAIN>` record + TLS cert points every
  world host at the deployment (resolve check)
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

- `proxyd/main.go`: world derivation + the `302` live in the final
  public catch-all (the former `http.Redirect(…, "/pub"+path)` site),
  never earlier — that placement is what makes reserved prefixes
  global and the redirect loop-free.
- `HOSTING_DOMAIN` is read from instance config (`core.Config`); empty
  → no world derivation (single-host deployments behave as before).
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

## One URL, one backing store

The slot model above is the only writer to `<data>/web/pub/`. There are
**no ownerless static trees** under it — every `/pub/<seg>/` URL is backed
by exactly one store. This closes the drift `5/N` (superseded, folded here)
flagged after the marinade `/pub/guides` incident, where one guide existed
at `pub/guides/`, `pub/atlas/`, and `pub/atlas/guides/` and a human rsync
kept three hand-copies that diverged. N file copies feeding one URL violates
"one renderer, many sinks" (CLAUDE.md).

1. **Every top-level segment of `/pub/` is owned by a group.** A group's
   slot projects to `<data>/web/pub/<folder>/`. The only writers are (a)
   group containers, each into its own slot, and (b) tier-0 root, which owns
   the top level and the shared frame `/pub/arizuko/` (root's `public_html`
   projects to the tree top).

2. **Cross-group / aliased URLs are redirects, never copies.** A top-level
   alias like `/pub/guides/` is a `web_routes` row
   `{path_prefix:/pub/guides/, access:redirect, redirect_to:/pub/atlas/guides/, folder:atlas}`.
   proxyd's longest-prefix match serves it; no second file tree exists.

3. **The agent publishes via an action, never by writing a path it cannot
   mount.** Publish = write into `~/public_html/` (its slot) + `set_web_route`
   for any alias. The agent owns content at `pub/<folder>/...` and points the
   alias at it — it never needs `pub/guides/` on its filesystem. This is the
   MCP+REST-uniform publish path.

4. **Top-level prefix ownership is explicit and first-claim.** `set_web_route`
   (`ipc/ipc.go`) constrains `redirect_to` to the caller's slot but leaves
   `path_prefix` open. The rule: a `web_routes` row whose `path_prefix` is a
   top-level prefix outside the caller's own `/pub/<folder>/` is allowed only
   if unclaimed (no existing row), recorded with `folder` = claimant. The
   `0068` FK (`web_routes.folder → groups`, CASCADE) retires the claim when
   the owner dies. Operator-curated top-level paths (`/pub/arizuko/`,
   marketing `/pub/index.html`) are root-owned, declared in the instance
   manifest's `web_routes` (`5/36`, `owner: system`).

Operational consequence: the `template/web/pub/` rsync target is
`<data>/web/pub/arizuko/` only (root slot) — no "rsync to any subdir of
`web/pub/`" affordance. The operational cleanup shipped on all instances;
code enforcement of the path-claim constraint is tracked separately.

## Related

- `specs/3/5-tool-authorization.md` — tier model, mount table
- `specs/3/8-web-virtual-hosts.md` — older vhost spec, superseded
  here for the web-slot model
- `specs/4/2-proxyd.md` — `/pub/*` and `/priv/*` route handling
