---
status: defected
defects: [F62, F63]
---

# Web Virtual Hosts + Agent Web Slots

Per-group writable web slots in the agent's home, served through proxyd.
Each world is reachable at a **derived** hostname that redirects to its
public slot — no per-host config, no vhost file, no DB table.

## Problem

One instance hosts many worlds; each needs its own hostname without
per-world configuration in the proxy, and a place to publish web content —
public for everyone, private behind OAuth — without growing platform
mechanism per group. The original `vhosts.json` map was the only
web-routing mechanism that was neither derived nor DB-backed: a hand-edited
file, out of step with the rest of the routing surface, and it drifted (spec
said `301`, code did an in-place rewrite). It is retired, along with the
`vhosts` struct and its mtime-reload loop.

## Hostname → world is derived, never configured

The deployment sets one `HOSTING_DOMAIN` (krons: `fiu.wtf`); world `W` is
reached at `W.<HOSTING_DOMAIN>`, and proxyd issues a same-origin `302` to
`/pub/W/`, preserving sub-path and query.

This keeps "identity is configured, never derived" (CLAUDE.md) honest: the
world _name_ (its folder) and `HOSTING_DOMAIN` are both configured; the
host is only their composition.

Four constraints, each closing a specific hole:

- **Boundary-checked suffix removal**, not `HasSuffix`: the host must equal
  `W + "." + HOSTING_DOMAIN` after lower-casing, port-stripping, and
  trimming a trailing dot — so `notkrons.fiu.wtf` never maps to `krons`.
- **`W` must be a single label** (worlds are single-label folders).
  Anything else falls through to normal routing.
- **No existence lookup.** A single-label subdomain maps to `/pub/<W>/`,
  which simply 404s if no such world exists. Bare `HOSTING_DOMAIN`
  (`W==""`) is un-mapped — never `/pub//`.
- **`Location` is always a relative `/pub/<W>/…` path**, never an absolute
  URL, so an attacker-supplied `Host` cannot open-redirect.

### Alias override

When a world must be reached at a host whose label is not its folder name,
`WEB_VHOST_ALIASES` (env, `host=world,host2=world2`) is consulted **before**
derivation — marinade serves world `atlas` at `fab.krons.cx`. Aliases are
still just host→world → `302 /pub/world/`. They are operator env, not a
hand-edited file in the web tree: the small configured exception, not a
return of `vhosts.json`.

## Where the redirect runs — and why it can't loop

The world-redirect replaces **only the final public catch-all** in
`proxyd/main.go` (the former `http.Redirect(…, "/pub"+path)` site). This
placement is the whole safety argument:

- Every reserved surface is dispatched BEFORE that point, so it is served
  in place on every vhost.
- The 302 lands on `/pub/W/…`; the follow-up request — same host — enters
  the `/pub/` branch and stops. A reserved prefix never re-enters the
  catch-all, so the redirect cannot loop.
- Running the derivation any earlier would both reintroduce loop risk and
  shadow the reserved surfaces.

The `..` / `%2e%2e` / `%2f` rejection that guarded the old in-place rewrite
moved onto the redirect builder.

**Reserved prefixes are global.** `/auth /dash /pub /priv /dav /chat /hook
/me /x /api /health` and every declared route backend prefix are served
identically on **every** vhost, because they match ahead of the catch-all.
So `krons.fiu.wtf/dash/` and `/auth/login` work from any world's hostname —
no separate dashboard domain. A world's hostname is just a friendly front
door to its `/pub/<W>/` slot; the platform surfaces are everywhere.

### Layering

The derived host-redirect is the OUTER layer; the DB-backed redirects are
INNER and unchanged:

1. `W.<HOSTING_DOMAIN>/<path>` → 302 → `/pub/W/<path>` (derived).
2. `/pub/W/<path>` → longest-prefix `web_routes` match
   (`redirect`/`deny`/`auth`), else vited serves the file.
3. vited serves `<data>/web/pub/W/<path>`, resolving `index.html` for a
   trailing-slash directory request.

The one residual loop risk is a `web_routes` row redirecting back to a
non-reserved same-host path — the agent's own misconfiguration, bounded to
its own slot by `set_web_route`.

## Discovery — `get_web_presence`

A world derives its hostname but is never told it. `get_web_presence` is
the read-only surface that closes that gap: given a folder it reports the
hosting domain, derived host, any alias, the canonical host, and the
public/private base URLs.

`pub_path` (`/pub/<folder>/`) is the vhost-independent canonical answer —
it works even when no `HOSTING_DOMAIN` or alias is configured.

Two invariants: **one shared renderer** (`ipc.WebPresenceFor`,
`ipc/ipc.go:717`) feeds both the MCP tool (`ipc/ipc.go:2235`) and the REST
twin (`GET /v1/web_presence`, `routd/web_routes_http.go:23`), so the faces
cannot drift; and both enforce the same containment — an elevated `/root`
turn may query any folder, everyone else only its own folder or a
descendant.

## Agent web slots

Every group container with the `web:publish` grant gets two writable slots
in its home plus a read-only view of the unified public tree
(`container/runner.go:604-632`). **No grant → no web surface at all** —
this is a grant decision, not a rank: the all-groups `/var/lib/groups`
mount is separately gated on operator elevation.

| Container path    | URL served at         | Auth      | Bind-mount source            |
| ----------------- | --------------------- | --------- | ---------------------------- |
| `~/public_html/`  | `/pub/<folder>/…`     | none      | `<data>/web/pub/<folder>/`   |
| `~/private_html/` | `/priv/<folder>/…`    | OAuth/JWT | `<data>/web/priv/<folder>/`  |
| `/var/lib/www/`   | (read access, no URL) | n/a       | `<data>/web/pub/` (RO whole) |

The `/pub/<X>` and `/<X>` URLs land on the SAME file. `/priv/<X>` serves a
DIFFERENT tree; content under `web/priv/` is NEVER served via `/pub/`.

**Bind-mount, not symlink.** The unified tree at `<data>/web/{pub,priv}/`
is canonical filesystem that vited/webd serve from directly; each container
gets a bind-mount VIEW of its own subdir. The same bytes appear at two
paths because the kernel binds them.

**Nested subgroups keep hierarchy in URLs.** `atlas/support` mounts from
`<data>/web/pub/atlas/support/` — a real subpath of atlas's own tree, so it
serves at `/pub/atlas/support/…`. Inside the subgroup's container its own
bind mount takes precedence; from atlas's container the same content shows
up read-only under `/var/lib/www/atlas/support/`, so atlas sees it and
knows not to overwrite.

**Truly private storage:** `~/workspace/`, `~/diary/`, `~/facts/`,
`~/users/`, `~/.claude/` and other `~/*` subdirs are bind-mounted into no
web tree. They have no URL.

## Platform mount paths

Platform mounts use FHS canonical locations. The previous `/workspace/*`
prefix was a devcontainer convention misapplied.

| Container         | Host                           | Mode                    |
| ----------------- | ------------------------------ | ----------------------- |
| `/opt/arizuko`    | `<repo>`                       | RO                      |
| `/var/lib/www`    | `<data>/web/pub/`              | RO (with `web:publish`) |
| `/run/ipc`        | `<data>/ipc/<folder>/`         | RW                      |
| `/var/lib/share`  | `<data>/groups/<world>/share/` | RO/RW per grant         |
| `/var/lib/groups` | `<data>/groups/`               | RW, elevated turn only  |
| `/mnt/<name>`     | operator extras                | varies                  |
| `/home/node/`     | `<data>/groups/<folder>/`      | RW (group home)         |

An elevated `/root` turn also gets an `infra` skill for instance-level
setup: setting `HOSTING_DOMAIN` once, the wildcard `*.<HOSTING_DOMAIN>` DNS
record plus TLS cert, and the web directory structure.

## One URL, one backing store

The slot model is the only writer to `<data>/web/pub/`. There are **no
ownerless static trees** under it — every `/pub/<seg>/` URL is backed by
exactly one store.

This closes a real failure: on marinade one guide existed at `pub/guides/`,
`pub/atlas/`, and `pub/atlas/guides/`, and a human rsync kept three
hand-copies that diverged. N file copies feeding one URL violates "one
renderer, many sinks" (CLAUDE.md).

1. **Every top-level segment of `/pub/` is owned by a group.** The only
   writers are group containers, each into its own slot, and an elevated
   `/root` turn, which owns the top level and the shared frame
   `/pub/arizuko/`.
2. **Cross-group / aliased URLs are redirects, never copies.** A top-level
   alias like `/pub/guides/` is a `web_routes` row with
   `access: redirect, redirect_to: /pub/atlas/guides/`. Longest-prefix
   match serves it; no second file tree exists.
3. **The agent publishes via an action, never by writing a path it cannot
   mount.** Publish = write into `~/public_html/` + `set_web_route` for any
   alias. The agent never needs `pub/guides/` on its filesystem.
4. **Top-level prefix ownership is explicit and first-claim.**
   `set_web_route` constrains `redirect_to` to the caller's slot but leaves
   `path_prefix` open. A row whose `path_prefix` is a top-level prefix
   outside the caller's own `/pub/<folder>/` is allowed only if unclaimed,
   recorded with `folder` = claimant. The `0068` FK
   (`web_routes.folder → groups`, CASCADE) retires the claim when the owner
   dies. Operator-curated paths (`/pub/arizuko/`, marketing
   `/pub/index.html`) are system-owned, declared in the manifest
   ([`8-yaml-manifests.md`](8-yaml-manifests.md)).

Operational consequence: the `template/web/pub/` rsync target is
`<data>/web/pub/arizuko/` only — there is no "rsync to any subdir of
`web/pub/`" affordance. The operational cleanup shipped on all instances;
code enforcement of the path-claim constraint is tracked separately.

## Related

- [`../3/5-tool-authorization.md`](../3/5-tool-authorization.md) — mount table
- `../3/8-web-virtual-hosts.md` — older vhost
  spec, superseded here
- [`../4/2-proxyd.md`](../4/2-proxyd.md) — `/pub/*` and `/priv/*` handling
