---
status: shipped
---

# Topic routing — inline `@agent` and `#topic`

Both are handled entirely in the **prefix layer** — code, not rows. The
routes table never stores `@` or `#` entries.

That is the decision. A prefix is relative to the folder that owns the
incoming room, so storing it would mean duplicating a row per group and
rewriting every one of them whenever a group moved. Keeping it in code
means the prefix layer resolves against whatever folder currently owns
the message, and there is nothing to migrate.

Layer order: sticky → command → prefix → routing
([`../1/F-group-routing.md`](../1/F-group-routing.md)). Only the last
reads the routes table.

## `@agent` — different container

`@support hello` delegates to the child folder
`<owning-folder>/support`. The child must already exist in the groups
table; if it doesn't, the prefix is **not consumed** and the message
falls through to the routing layer unchanged. Silently swallowing
`@nope` would make a typo look like a delivery failure — and `@`
collides with mentions, cross-instance references, and ordinary prose in
several languages, so a name miss must not eat the message.

## `#topic` — same container, different session

`#deploy let's review` routes to the same folder with topic `deploy`,
creating or resuming a session keyed `(folder, topic)`
(`store.GetSession` / `SetSession`, default callers pass `""`). The
prefix **is** consumed — the agent sees `let's review`. The topic
travels in `container.Input.Topic` and shows up as a
`Topic session: #deploy` annotation.

|              | `@agent`                  | `#topic`                      |
| ------------ | ------------------------- | ----------------------------- |
| Routes to    | different group/container | same group, different session |
| Agent config | can differ                | same                          |
| Context      | separate                  | separate                      |

## Known limitation: batch ordering

Prefix resolution runs on the **last** non-command message in a poll
batch, so if several messages arrive together carrying different
prefixes, the last one decides the target for the whole batch. Earlier
prefix content is not lost — it still reaches the container in the
message context — but its routing intent is. Per-message resolution
before batching is the fix if this ever bites.

## Commands

`/new #topic msg` clears that named session and routes the message to
it. `/stop` is folder-wide; there is no `/stop #topic`.

## Not in scope

Agent-created topics, topic ACLs, a topic listing command, cross-group
topic routing, pipeline/DAG routing between topics.
