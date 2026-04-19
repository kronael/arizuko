# ipc — Security

The MCP channel: how agents reach gateway tools over a unix socket, how
group isolation is enforced, and what the peer-uid check is for. See
[`../SECURITY.md`](../SECURITY.md) for the system-wide model.

## Threat model

| Threat                                                | Defense                                        |
| ----------------------------------------------------- | ---------------------------------------------- |
| Agent in group A reads group B's socket               | Per-group bind mount — B's socket not visible  |
| Host process as uid 1000 connects to a group's socket | `SO_PEERCRED` peer-uid check (kernel-attested) |
| Sidecar in same group impersonates the agent          | Out of scope — same group, same trust domain   |
| Host root opens any socket                            | Out of scope — host root is trusted            |
| Peer writes fake MCP messages after a valid connect   | Out of scope — stream auth not layered on MCP  |

## Security boundary

The real boundary is **per-group bind mount**. `container/runner.go
buildMounts` mounts only the current group's `ipcDir` at
`/workspace/ipc`:

```go
ipcDir, err := folders.IpcPath(in.Folder)
if err == nil {
    m = append(m, volumeMount{
        Host:      hp(cfg, ipcDir),
        Container: "/workspace/ipc",
    })
}
```

`folders.IpcPath` rejects `..`, empty segments, and reserved names
(`share`, `*`, `**`). Other groups' `ipcDir` are unreachable — no path
inside the container resolves to them.

## Sanity gate: SO_PEERCRED

On every accepted connection, `ipc.ServeMCP` reads the peer's
kernel-attested credentials via `SO_PEERCRED` and rejects any peer
whose uid doesn't match the expected container uid (1000 in prod, or
the host uid when `--user` override fires in dev):

```go
if expectedUID > 0 {
    cred, err := peerCred(c)
    if err != nil { slog.Warn("mcp peer cred read failed"); return }
    if int(cred.Uid) != expectedUID {
        slog.Warn("mcp peer uid mismatch",
            "want", expectedUID, "got", cred.Uid, "pid", cred.Pid)
        return
    }
}
```

Properties:

- **No client changes.** Standard MCP. Socat bridge unmodified.
- **Kernel-attested.** Not spoofable; `SO_PEERCRED` is set at connect.
- **Cheap.** One syscall per connection.
- **Fails closed** by default in production (expectedUID = 1000). Set
  `≤ 0` only in unit tests where caller runs in-process.

Tests: `TestServeMCP_PeerCredAcceptsMatchingUID`,
`TestServeMCP_PeerCredRejectsWrongUID` in `ipc/ipc_test.go`.

## Incident 2026-04-17 → 2026-04-19 — token preamble outage

Two days in which agents silently lost every gateway tool (`get_history`,
`send_file`, `reset_session`, delegation, …) while appearing healthy.
Symptom: agents replying "no context" — looked like amnesia, was total
MCP failure.

## What broke

Commit `2774394` ("18-bucket audit") added a per-connection token
preamble to `ipc.ServeMCP`. Every connection had to write
`{"token":"<hex>"}\n` before JSON-RPC:

```go
// ipc/ipc.go (pre-v0.29.3)
if token != "" {
    if _, err := verifyToken(c, token); err != nil {
        slog.Warn("mcp token verification failed")
        return
    }
}
```

Server side enforced it. Client side didn't exist. The agent reaches
the socket through a socat stdio bridge (`ant/src/index.ts`,
`container/runner.go`):

```json
"arizuko": {
  "command": "socat",
  "args": ["STDIO", "UNIX-CONNECT:/workspace/ipc/gated.sock"]
}
```

socat pipes stdio straight to the socket — no hook to emit a preamble.
The MCP SDK opened the connection, wrote `initialize`, the server
parsed it as a token, rejected, closed.

Result path goes through stdout, not MCP, so agents ran normally for
anything not needing memory or delegation. Gateway logged `mcp token
verification failed` on every spawn; nobody was reading.

## Why the hardening was wrong

1. **Untested end-to-end.** Server side had unit tests; no integration
   test ever dialed the socket through the ant runtime.
2. **Threat model unstated.** "Auth hardening" didn't name what the
   token defended against that mount isolation didn't. The real answer
   — "same-uid sidecar in the same group" — is narrow and was already
   out of scope (see threat table).
3. **Client side orphaned.** `ARIZUKO_MCP_TOKEN` was generated, stamped
   into container env, then ignored — no ant code to write it.

Shipping server verification with no client implementation guarantees a
silent outage on first deploy.

## Audit conclusions

| #   | Finding                                                                       | Status              |
| --- | ----------------------------------------------------------------------------- | ------------------- |
| 1   | Token preamble broke every MCP call in production                             | fixed v0.29.3       |
| 2   | No integration test covered an agent→gateway MCP roundtrip                    | still open          |
| 3   | `SO_PEERCRED` is a sufficient sanity check; no token needed                   | implemented v0.29.4 |
| 4   | Dead token plumbing (`verifyToken`, `McpToken`, env stamp) left behind        | removed v0.29.4     |
| 5   | Broad audit commits shouldn't ship protocol changes without client+server+e2e | process note        |

## Lessons

- **Half a protocol is worse than none.** An e2e test would have caught
  this at commit time.
- **Don't layer auth onto a transport that's already scoped.** The
  socket is scoped by mount; a preamble added a second failure mode
  with no new safety.
- **Name the threat in the commit message.** "Prevent same-uid host
  processes from speaking MCP" would have prompted "how does the client
  send the preamble?" before merge.
- **Prefer kernel primitives to handshakes.** `SO_PEERCRED` is one
  syscall, no protocol change, not spoofable. Handshakes drift; the
  drift is invisible until it matters.
- **Logs nobody reads aren't observability.** Gateway logged the
  rejection hundreds of times per day for two days. A successful agent
  boot should imply a successful MCP handshake, enforced by
  healthcheck.

## Follow-ups

- e2e test: spawn container, call `get_history` over MCP, assert
  non-empty. (tracked in `bugs.md`)
- `/health/mcp` on gated: self-dial `gated.sock`, call `ping`, 200 only
  if tool responds within deadline.
- Per-group gid on the socket if host processes outside the container
  uid ever need to speak MCP. Not required today.
