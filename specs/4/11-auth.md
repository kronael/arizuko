---
status: shipped
---

# Outbound JID authorization

> Everything else this spec once carried has moved. Token signing is
> ES256 in [`../5/1-auth-standalone.md`](../5/1-auth-standalone.md) (the
> HMAC model is retired). Web login, OAuth providers, and the
> `auth_users`/`auth_sessions` tables are
> [`../1/f-auth-oauth.md`](../1/f-auth-oauth.md). Tier-gated tool access
> is dissolved — [`../5/33-paths-roles.md`](../5/33-paths-roles.md).
> What survives is the one rule below, which is orthogonal to all of it.

Outbound chat verbs (`send`, `send_file`, `reply`, `post`, `like`,
`dislike`, `delete`, `edit`, `forward`, `quote`, `repost`) resolve the
target JID's owning folder and thread it through `Authorize` as
`AuthzTarget{TargetFolder: ...}`. The folder comes from the routes table
at the IPC layer (`store.DefaultFolderForJID`).

The rule is plain subtree containment: a caller in folder `X` may
address chats whose folder is `X` or under `X/...`. Unrouted JIDs are
denied for every caller.

## No tier bypass — including root

Even the instance root cannot direct-send to a JID that routes to a
different world. This is the part worth remembering, because it is the
opposite of how the rest of the authorization model reads: elsewhere,
more authority means more reach.

It closed a live hole. During a release-broadcast fan-out, several
world-level agents independently called `send` at the same JIDs from
memory of past conversations, with no regard for which folder actually
owned the route — cross-world chats got spammed by agents that had
never been routed to them. Reach-by-recall is the failure mode; binding
the verb to the route table is the fix.

Inter-world communication goes through `delegate_group` /
`escalate_group`, which leave an auditable hop.

## Why this lives at the verb, not the transport

The same containment could have been enforced when a message reaches an
adapter. It is enforced at the tool call instead, so the agent gets a
denial it can reason about and correct in-turn, rather than a message
that silently never arrives.
