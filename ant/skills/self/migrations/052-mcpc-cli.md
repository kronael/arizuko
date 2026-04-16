# 052 — mcpc for calling MCP tools from scripts

Scripts can call MCP tools via apify's `mcpc` (HTTPie-style) over the
local unix socket. `ARIZUKO_MCP_SOCKET` = `/workspace/ipc/gated.sock`.

```bash
mcpc connect "socat UNIX-CONNECT:$ARIZUKO_MCP_SOCKET -" @s
trap 'mcpc @s close' EXIT
mcpc @s tools-call send_message jid:="$JID" text:="hi"
```

Params: `key:=value` is JSON-typed, `key=value` is a string. Subcommands:
`tools-list`, `tools-call`, `resources-list`/`read`, `prompts-list`/`get`.
