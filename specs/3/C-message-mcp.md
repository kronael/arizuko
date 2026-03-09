# Message MCP -- v2

MCP tools for agent-side message history queries.

## Tools

### `get_history`

```
get_history(jid?, limit?, since?, until?)
```

Returns messages as XML (`<messages>` format). For old
message lookup, reply sources outside sliding window,
thread history.

### `get_thread`

```
get_thread(jid)
```

All messages in a channel thread (Discord, email, Telegram
forum topic).

## Channel hierarchy context

With hierarchical JIDs: `get_history("discord/srv/ch/*")`.
For v2, parent channel ID passed explicitly.

## Open

- MCP sidecar interface design
- Auth: agent can only query own group's messages
