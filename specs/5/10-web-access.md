---
status: shipped
---

# Web access: `/priv` is a grant decision, not a second ACL

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

## Shipped (2026-06-15, corrected 2026-08-05)

The `/priv/*` branch in `proxyd/main.go`. After auth stamps `X-User-Groups`,
call `auth.MatchSlot(gs, slotPath)` on the whole path after `/priv/`; 403 on no
match, proxy to vited otherwise.

Resolved along the way:

- **Read vs list** — vited serves files, not directory listings, so there is no
  listing surface to scope separately.
- **Grant verb** — none added. Any grant covering the folder (`interact`,
  `admin`, `*`) passes; the existing scope vocabulary suffices.
- **Inheritance — NO.** The original text claimed a grant on `atlas` covers
  `atlas/search` "via segment traversal". It does not, and must not: `5/33`
  decision 8 makes the scope glob the containment, so `atlas` is one folder and
  `atlas/**` is the subtree. This spec's claim was the outlier that let two
  callers grow private prefix walks (BUGS `F4`).

  A **slot path** is still not a folder, and that is the part the first
  implementation got wrong. It cut the URL at segment one, so `atlas/search`
  — a real, multi-segment folder — was 403'd on its own private page, while a
  scope of `atlas/*` reached nothing. `auth.MatchSlot` resolves the owner by
  testing every path prefix against `MatchGroups`. That is `5/V` **filesystem**
  containment, not grant inheritance: `container/runner.go` bind-mounts
  `web/priv/<folder>`, so folder `atlas`'s `~/private_html` physically holds
  `atlas/search`'s slot and every byte under it. Denying over HTTP what the
  parent's own mount already hands it would be theater — and would break the
  ordinary case of `atlas` publishing `~/private_html/reports/q3.html`.
  Folder decisions elsewhere use `MatchGroups` and never cross a segment the
  glob did not ask for.

## What this is not

Not configurable bind-mounts (the container mount model in `5/V` is unchanged).
Not a new web-ACL table. Not a change to `/pub` or vhost derivation — a world's
vhost still serves its own `/pub/<world>/` slot, and must NOT map to the shared
`/pub/` root, which holds every tenant plus the docs site.
