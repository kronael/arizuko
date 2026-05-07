---
name: mcp
description: Reference for calling arizuko MCP tools from scripts via `mcpc` over `$ARIZUKO_MCP_SOCKET`.
when_to_use: >
  Use when writing scheduled tasks, one-off scripts, or anything that needs to
  invoke an MCP tool outside the agent's live tool surface.
---

# MCP from scripts

The agent's MCP tools (`send`, `send_voice`, `inspect_messages`, …) are
also reachable from any script in the container via the unix socket at
`$ARIZUKO_MCP_SOCKET` (`/workspace/ipc/gated.sock`). Use `mcpc` (apify)
as the wire-protocol client; pipe the socket through `socat`.

`key:=value` is JSON-typed (numbers, bools, raw JSON), `key=value` is
plain string.

## Bash

```bash
set -e
mcpc connect "socat UNIX-CONNECT:$ARIZUKO_MCP_SOCKET -" @s
trap 'mcpc @s close' EXIT

mcpc @s tools-list
mcpc @s tools-call send chatJid:="telegram:user/<id>" text:="hello"
```

## Python

```python
import os, subprocess

JID = "telegram:group/<id>"
sock = os.environ["ARIZUKO_MCP_SOCKET"]
subprocess.run(
    ["mcpc", "connect", f"socat UNIX-CONNECT:{sock} -", "@s"],
    check=True,
)
try:
    r = subprocess.run(
        ["mcpc", "@s", "tools-call", "send", f"chatJid:={JID}", "text:=hi"],
        check=True, capture_output=True, text=True,
    )
    print(r.stdout)
finally:
    subprocess.run(["mcpc", "@s", "close"], check=False)
```

## Go

```go
package main

import (
    "os"
    "os/exec"
)

func main() {
    sock := os.Getenv("ARIZUKO_MCP_SOCKET")
    must(exec.Command("mcpc", "connect",
        "socat UNIX-CONNECT:"+sock+" -", "@s").Run())
    defer exec.Command("mcpc", "@s", "close").Run()

    must(exec.Command("mcpc", "@s", "tools-call", "send",
        "chatJid:=discord:dm/<channel>", "text:=hello").Run())
}
func must(err error) { if err != nil { panic(err) } }
```

## Notes

- Socket is container-bound; scripts inherit the agent's folder/grants.
- chatJid format: `/typed-jids`. Bare ids (`telegram:1234`) are stale.
- Scheduled tasks run as fresh agent contexts, not scripts — call
  `mcpc` from inside a task only if the prompt drives shell directly.
