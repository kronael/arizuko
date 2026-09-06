# mcpfw — a transparent MCP tool-call filter

Sits between an agent and its MCP servers, parses JSON-RPC, and allows,
denies or logs each `tools/call` against a flat ruleset. It runs on its own:
no arizuko process, no data directory, no database.

```bash
mcpfw serve --upstream http://localhost:7071 --rules rules.json --listen :7070
```

## Ruleset

A flat list over opaque tool-name globs. `*` spans any run of characters.

```json
{
  "rules": [
    { "tool": "read_*", "action": "allow" },
    { "tool": "send_*", "action": "deny" }
  ]
}
```

**Deny-wins, default-deny.** Every rule is evaluated; one matching `deny`
refuses the call whatever else allows it, and a tool matched by no `allow`
is refused. A later broad `allow: *` never re-opens a denied tool.

That is the opposite of a first-match route table, and deliberately so:
routing is positive selection, gating is negative authority, and the safe
failure for a gate is "refuse".

An `action: log` records the call and decides nothing on its own.

## Flags

| flag            | meaning                             |
| --------------- | ----------------------------------- |
| `--upstream`    | MCP server URL to gate (required)   |
| `--rules`       | ruleset JSON file (required)        |
| `--listen`      | listen address, default `:7070`     |
| `--filter-list` | hide denied tools from `tools/list` |

`--filter-list` is off by default: hiding a tool changes what the model
believes exists, which is a choice to make rather than a side effect of
gating.

## What it does not own

No folders, no grants, no identity, no tenancy. Every signature takes
strings and the flat `Rule` shape. A caller that needs scoped policy derives
the flat tool list itself and writes it as the ruleset file `serve` reads at
startup — one process per ruleset. An in-process caller skips the binary and
calls `firewall.Firewall.Handle`. See `specs/6/12-mcp-firewall.md`.

## Layout

    cmd/mcpfw/          the binary
    pkg/rule/           ruleset model and matching
    pkg/firewall/       the JSON-RPC gate
    internal/transport/ JSON-RPC over HTTP
    testdata/           fixtures

`make build` · `make test` · `make lint`
