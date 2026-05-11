---
name: mcp
description: >
  Call arizuko MCP tools from scripts via `mcpc` over
  `$ARIZUKO_MCP_SOCKET`. USE for "scheduled task", "call MCP from
  script", `mcpc` invocation, cron-driven message sends, one-off scripts
  that hit MCP tools. NOT for the live in-session MCP calls (those are
  direct tools).
user-invocable: true
---

# MCP

The agent's MCP tools (`send`, `send_voice`, `inspect_messages`, …) are
reachable from any script in the container via the unix socket at
`$ARIZUKO_MCP_SOCKET` (`/workspace/ipc/gated.sock`). Use `mcpc` (apify) as the
wire-protocol client; pipe the socket through `socat`.

Args: `key:=value` is JSON-typed (numbers, bools, raw JSON), `key=value` is
plain string.

```bash
set -e
mcpc connect "socat UNIX-CONNECT:$ARIZUKO_MCP_SOCKET -" @s
trap 'mcpc @s close' EXIT

mcpc @s tools-list
mcpc @s tools-call send chatJid:="telegram:user/<id>" text:="hello"
```

Same pattern from any language: spawn `mcpc connect`, run `tools-call`,
close on exit. Python `subprocess.run` and Go `exec.Command` shell out
identically — no language-specific binding needed.

## Notes

- Socket is container-bound; scripts inherit the agent's folder/grants.
- chatJid format: `/typed-jids`. Bare ids (`telegram:1234`) are stale.
- Scheduled tasks run as fresh agent contexts, not scripts — call `mcpc` from
  inside a task only if the prompt drives shell directly.
