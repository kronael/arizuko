---
status: draft
---

# Web access: grant-gated content, configurable cross-group sharing

DRAFT — proposes generalizing web _access_ (who may read which web
content) without changing web _ownership_ (the per-folder slot model).
Sibling of [`5/V`](V-web-vhosts.md) (vhosts + slots) and
[`4/9`](../4/9-acl-unified.md) (the unified `Authorize` grant gate).

## Problem

Web ownership is already clean and minimal — each group owns
`~/public_html/` → `/pub/<folder>/` and `~/private_html/` →
`/priv/<folder>/` (`5/V`), and a world is reached at its derived vhost
(`<folder>.<HOSTING_DOMAIN>` → `/pub/<folder>/`). That part should not
change.

Web **access** is the gap. It has exactly two settings:

- `/pub/*` — public to everyone.
- `/priv/*` — **any logged-in user** (`proxyd` `requireAuth`,
  `proxyd/main.go:574`). NOT folder-scoped: a logged-in user with no
  grant on `atlas` can read `/priv/atlas/...`.

So you cannot express the natural operator wishes that surfaced
reorganizing atlas:

- "the `research` group may read `atlas/search`'s private content" —
  there is no per-folder web grant; `/priv` is all-or-nothing-logged-in.
- "this guide belongs to `atlas/search`, shared with `research`" — the
  only tools today are _move the file_ (ownership) or _make it public_.
  Cross-group sharing has no expression short of copying files (which
  `5/V` "one URL, one backing store" forbids) or a redirect.

The binary model is "pretty bad but inevitable" only because access was
never routed through the primitive that already answers "may principal
P do X to folder F" — the unified `Authorize` grant gate.

## Approach — access IS a grant, reuse the one gate

The platform already has exactly one authority for "may this principal
touch this folder": `auth.Authorize` / the grant DSL (`4/9`), enforced
as uniform middleware (CLAUDE.md "auth is a uniform middleware, bound to
handler + params"). Chat reads, MCP tools, and REST all bind a target
folder to the caller's grants. **Web reads are the one surface that
doesn't.** Close that — don't invent a second mount/ACL system.

### Ownership unchanged

`/pub/<folder>/` and `/priv/<folder>/` stay backed by the owning
group's slot (`5/V`). No new mount config, no file moves for sharing,
no per-path mount table. A folder owns its slot; full stop.

The indirection is deliberate and load-bearing: **inside the group the
agent sees only `~/public_html/`** — folder-agnostic, no awareness of
its own name; **on disk and in the URL it is `/pub/<folder>/`** (the
bind mount projects one onto the other, `5/V`). The agent never writes a
folder-prefixed path and never needs to; `get_web_presence` (`5/V`) is
how it learns its actual public URL. Access control must therefore key
on `<folder>` (resolved from the URL path / the slot's backing dir) —
the thing the agent doesn't see — NOT on anything the agent itself
supplies. That keeps ownership (agent-local, `~/public_html/`) and
access (platform-resolved, `<folder>`-scoped grant) cleanly orthogonal.

### Access becomes a folder-scoped grant decision

`proxyd` already resolves the caller's grant patterns into
`X-User-Groups` (`groupsForSub` → `st.UserScopes`,
`proxyd/main.go:782`). Today it computes them and only checks
operator-ness. Extend the `/priv/<folder>/...` handler to make ONE
structural decision: **serve iff the caller holds a read grant whose
scope ⊇ `<folder>`** — the same containment `Authorize` already does for
chat/MCP/REST. Reuse `auth.Authorize`/the scope-match helper; do not
hand-roll a path ACL.

Result, with zero new primitives:

| Want                                              | Expressed as                                                                                                                                                  |
| ------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| public page                                       | write to `~/public_html/` → `/pub/<folder>/` (today)                                                                                                          |
| private to the owning group                       | `/priv/<folder>/` + the owner's existing self-grant (today, now actually enforced per-folder)                                                                 |
| **research reads atlas/search's private content** | grant `research` principals a **read** scope on `atlas/search` (`arizuko grant` / `add_acl` / dashd) — the `/priv/atlas/search/...` gate then passes for them |
| operator sees all                                 | the `**` operator grant already ⊇ every folder                                                                                                                |

Cross-group sharing is a **grant**, never a mount or a copy. The file
stays in `atlas/search`'s slot; `research`'s access is a row in the same
ACL table that gates everything else. Revoke = drop the grant.

### Why this is minimal + orthogonal

- **One gate, more sinks.** `Authorize` already decides folder access
  for chat/MCP/REST; web `/priv` becomes the fourth sink of the same
  decision, not a parallel mechanism. (`5/5` "MCP + REST hand-rolled and
  uniform" extended to the web read path.)
- **No new config surface.** No mount table, no per-path ACL file, no
  "web access" DSL. The grant DSL (`4/9`) already expresses it.
- **Ownership and access stay orthogonal.** Slots answer _where content
  lives_; grants answer _who may read it_. Moving a guide under
  `atlas/search` (ownership) and granting `research` read (access) are
  two independent, separately-documented operations.
- **`/pub` stays dumb.** Public is public; only `/priv` consults grants.
  No per-request grant lookup on the hot public path.

## What this is NOT

- NOT configurable bind-mounts. The container mount model (`5/V`
  "platform mount paths") is fixed and fine; this changes only the
  proxyd read gate, not what's mounted where.
- NOT a new "web ACL" table. It is the existing `acl`/grant rows
  (`4/9`), queried by the existing `UserScopes`.
- NOT a change to `/pub` or to vhost derivation (`5/V`). A world's vhost
  still serves its own `/pub/<world>/` slot (per-tenant isolation — a
  vhost must NOT map to the shared `/pub/` root, which holds every
  tenant + the docs site).

## Code pointers

- `proxyd/main.go:574` — the `/priv/*` handler (`requireAuth`); the
  single edit site: add the folder-scoped grant check after auth, before
  proxying to vited. Extract `<folder>` from the path's first segment
  (mirror `groupfolder.JidFolder`'s grammar).
- `proxyd/main.go:782` `groupsForSub` / `st.UserScopes` — already
  resolves the caller's grant patterns; the gate matches `<folder>`
  against them (reuse the scope-containment helper from `auth`/`grants`,
  not a new matcher).
- `auth.Authorize` / `4/9` — the canonical containment decision to reuse.
- `5/V` — ownership/slot/vhost model this builds on (unchanged).

## Open questions (draft)

- Read vs list: does a folder grant gate _reading a known path_ only, or
  also _directory listing_? (vited serves files, not listings today — so
  read-only is the natural scope.)
- Grant verb: reuse an existing read scope (e.g. `chats:read` /
  `groups:read` family) or mint a `web:read` scope? Prefer reusing an
  existing read verb so the web surface needs no new vocabulary —
  decide against the `4/9` scope table before shipping.
- Inheritance: a grant on `atlas` ⊇ `atlas/search` already by scope
  containment — confirm that's the desired default (parent grant sees
  child web content) and document it as such.
