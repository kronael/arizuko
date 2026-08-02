---
status: partial
---

# Tenant self-service — invites and the org-chart framing

> **Mostly superseded; kept for the invites primitive and the vocabulary.**
> The arbitrary-depth org-chart is COLLAPSED by
> [`5/29`](29-worlds-guests-oauth.md) to World → Agent → Session. Depth is not
> load-bearing and does not confer grants —
> [`5/33`](33-paths-roles.md) removed the tier axis entirely. The credential
> model moved to [`5/14`](14-credentials.md); the ACL to
> [`5/32`](32-acl-unified.md).

## Vocabulary

A **group** is a folder identified by a path — a pure coordinate. A **topic** is
the transient work-unit overlaid on a group; many topics per group.

The org-chart mapping is the framing that still holds: an organization is a
world, a job description is a grant rule list, the mailroom is the routes table,
hiring is invite + grant, off-boarding is revoke. Reporting structure maps to
the folder hierarchy, but only as a _coordinate_ — it grants nothing.

## Invites (Phase B — shipped 2026-05)

`invites` — opaque tokens that produce a grant on acceptance. Schema:
`store/migrations/0032-invites-rewrite.sql`; resreg resource + `arizuko invite`
CLI + `invite_create` MCP tool + dashd `/dash/invites/`.

Two modes determined by `target_glob`:

- **Trailing slash** (`atlas/`) — **subgroup-create**: the recipient picks a
  username, `atlas/<username>` is created from `groups/atlas/prototype/`, and
  they are granted it.
- **No trailing slash** (`atlas/support`) — **join**: the recipient is granted
  the existing group directly.

`**` is a reserved folder name and is rejected as a `target_glob`.

Lifecycle: an issuer holding grants on `target_glob` mints a token → the
recipient opens `/invite/<token>` → OAuth → the token is consumed and the group
created or the grant issued → `used_count` increments and the row stays for
audit. A failure to write the grant rolls the consume back (`RestoreInvite`), so
an invite is never burned without its grant.

## Topic kinds (unbuilt)

Topics may carry a `kind` — `task`, `project`, `meeting`, `question`,
`discussion`, `incident`, or the default `thread`. Kind drives kind-specific
workflow verbs (`set_due` on a task, `set_attendees` on a meeting). It is
metadata on the topic node, **not** a hierarchy level. Not built.

## Phase status

- **A** (this spec) and **B** (invites) — shipped.
- **C** (folder/user-scope secrets layering) — shipped, but as
  [`5/14`](14-credentials.md)'s credential model, which supersedes this spec's
  §Secrets shape.
- **D** — shipped as a boolean `chats.is_group`
  (`store/migrations/0033-chat-is-group.sql`), NOT this spec's `chats.kind`
  enum. The per-adapter dm/group attestation table is superseded shape; the
  `chats.kind ∈ {dm, slink}` gate on user-secret overlay was dropped outright
  (what matters is whether the caller is a real user sub, not the chat shape).
- **E/F/G** (structural dashd UI, topic kinds + workflow verbs, cross-group
  topic ops) — unbuilt, scope decision pending.

## Out of scope

Time-bounded grants (`expires_at`), an audit log of permission changes,
on-call rotation for routing — all additive, ship later. Cross-org
collaboration is disallowed by design: worlds are isolation boundaries.
