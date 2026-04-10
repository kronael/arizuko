# 052 — arizuko-mcp CLI for scripts

Scripts running inside the agent container can now call MCP tools
without being the agent itself. `/usr/local/bin/arizuko-mcp` is a
minimal Python MCP client over the gated.sock unix socket.

## Usage

    arizuko-mcp message <jid> <text>
    arizuko-mcp file <jid> <path> [caption]
    arizuko-mcp --default-jid message <text>
    arizuko-mcp tools

## Replaces

The old bash `send-to-group` script (writing to
`/workspace/ipc/requests/`) was vestigial from pre-015. arizuko-mcp is
the proper replacement.
