# 052 — mcpc for calling MCP tools from scripts

Scripts running inside the agent container can call MCP tools without
being the agent itself, using apify's `mcpc` — a general HTTPie-style
MCP client installed globally via npm.

## Transport

`mcpc` uses stdio transport. To reach the gateway's MCP server on the
local unix socket, wrap with `socat`:

    mcpc connect "socat UNIX-CONNECT:$ARIZUKO_MCP_SOCKET -" @s
    trap 'mcpc @s close' EXIT
    mcpc @s tools-call send_message jid:="$JID" text:="hi"

`ARIZUKO_MCP_SOCKET` is set in the image env to
`/workspace/ipc/gated.sock`.

## Param grammar (HTTPie-style)

- `key:=value` — JSON-typed (numbers, bools, objects, arrays)
- `key=value` — plain string

Example: `count:=10 enabled:=true name="foo bar"`.

## Subcommands

- `mcpc @s tools-list` — enumerate available tools
- `mcpc @s tools-call <name> key:=val ...` — invoke a tool
- `mcpc @s resources-list` / `resources-read`
- `mcpc @s prompts-list` / `prompts-get`

## Replaces

The old bash `send-to-group` script (writing to the dead
`/workspace/ipc/requests/` queue, pre-mig-015) was the last remaining
caller for script-to-MCP. `mcpc` replaces it with an orthogonal,
general-purpose tool — no arizuko-specific wrapper needed.
