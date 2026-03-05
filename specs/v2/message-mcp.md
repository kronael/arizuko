# Message MCP — v2

MCP tools for agent-side message history queries. Extends the current
sliding-window-only model to allow on-demand lookups.

## Tools

### `get_history`

Query messages DB from the agent. Gateway exposes via MCP sidecar.

```
get_history(jid?, limit?, since?, until?)
```

Returns messages as XML matching the existing `<messages>` format.
Agent uses this to look up old messages — including `in_reply_to` source
messages not in the current sliding window, thread history, etc.

### `get_thread`

Fetch all messages in a channel thread (Discord thread, email thread,
Telegram forum topic). Useful when agent is invoked in a thread and needs
full thread context beyond what's in the sliding window.

```
get_thread(jid)
```

## Channel hierarchy context

When agent is in a Discord thread channel or Telegram forum topic, it
should be able to query the parent channel's history. With hierarchical
JIDs (v3) this becomes `get_history("discord/serverid/channelid/*")`.
For v2, parent channel ID is available via `msg.channel.parentId` and
can be passed explicitly.

## Relationship to IPC

`get_history` was originally specced as an IPC call (`specs/v1/systems.md`).
MCP is the cleaner interface — agent calls a tool, gateway queries DB,
returns results. No custom IPC protocol needed.

## Open

- Design MCP sidecar interface (see `specs/v1/plugins.md`)
- Decide auth model — agent can only query its own group's messages
