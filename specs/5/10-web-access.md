---
status: partial
---

# Web access: `/priv` is a grant decision, not a second ACL

> **Status (2026-08-05).** Partial. The inheritance claim below describes a
> mechanism `auth.MatchGroups` does not have: it matches segment-for-segment and
> requires equal depth, so a grant on `atlas` does **not** cover `atlas/search`
> — only `atlas/**` or `**` do. The promised reach exists, but each caller
> reinvents it separately (dashd loops over prefixes, proxyd truncates the path
> to its first segment before matching), which is the drift this spec set out to
> prevent. BUGS `F4`.

Sibling of [`5/V`](V-web-vhosts.md) (vhosts + slots — **ownership**) and
[`5/32`](32-acl-unified.md) (the `Authorize` gate — **access**).

## Problem

Web ownership was already clean: each group owns `~/public_html/` →
`/pub/<folder>/` and `~/private_html/` → `/priv/<folder>/`. Access was not.
`/pub/*` was public and `/priv/*` was **any logged-in user** — not
folder-scoped, so a logged-in user with no grant on `atlas` could read
`/priv/atlas/…`.

That made two natural operator wishes inexpressible: "the `research` group may
read `atlas/search`'s private content", and "this guide belongs to
`atlas/search`, shared with `research`". The only tools were move-the-file
(ownership) or make-it-public. Copying is forbidden by `5/V`'s "one URL, one
backing store".

The binary model was inevitable only because web reads were the one surface that
never went through the primitive already answering "may principal P touch folder
F".

## Decision — reuse the one gate

`/priv/<folder>/…` serves **iff the caller holds a grant whose scope covers
`<folder>`** — the same containment `Authorize` does for chat, MCP, and REST.
No new mount config, no per-path ACL table, no `web:read` verb.

Cross-group sharing is therefore a **grant**, never a mount or a copy: the file
stays in `atlas/search`'s slot and `research`'s access is a row in the same ACL
table that gates everything else. Revoke = drop the grant. The `**` operator
grant already covers every folder.

**Ownership stays untouched.** The indirection is load-bearing: inside the
container the agent sees only `~/public_html/` — folder-agnostic, unaware of its
own name — while on disk and in the URL it is `/pub/<folder>/`. So the access
check must key on `<folder>` resolved from the URL path, **never on anything the
agent supplies**. Ownership is agent-local; access is platform-resolved.

**`/pub` stays dumb.** Public is public — no per-request grant lookup on the hot
public path.

## Shipped (2026-06-15)

One edit, `proxyd/main.go:589` — the `/priv/*` branch. After auth stamps
`X-User-Groups`, extract `<folder>` from the first path segment and call
`auth.MatchGroups(gs, folder)` (`proxyd/main.go:601`), the same containment
helper the WebDAV handler uses; 403 on no match, proxy to vited otherwise.

Resolved along the way:

- **Read vs list** — vited serves files, not directory listings, so there is no
  listing surface to scope separately.
- **Grant verb** — none added. Any grant covering the folder (`interact`,
  `admin`, `*`) passes; the existing scope vocabulary suffices.
- **Inheritance** — a grant on `atlas` covers `atlas/search` via segment
  traversal. Intended, not incidental.

## What this is not

Not configurable bind-mounts (the container mount model in `5/V` is unchanged).
Not a new web-ACL table. Not a change to `/pub` or vhost derivation — a world's
vhost still serves its own `/pub/<world>/` slot, and must NOT map to the shared
`/pub/` root, which holds every tenant plus the docs site.
