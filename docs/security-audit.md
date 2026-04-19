# Security Audit — MCP IPC Hardening

_2026-04-19 · arizuko v0.29.4_

Record of an audit and fix for the gateway↔agent MCP channel. Written
after a 2-day production incident in which agents silently lost access
to every gateway tool (`get_history`, `get_facts`, `search_memories`,
`send_file`, `reset_session`, cross-group delegation, …) while appearing
healthy. The symptom was agents replying "nemám záznam", "no context" —
seemingly amnesia, actually complete MCP failure.

## What broke

Commit `2774394` (2026-04-17 "18-bucket audit: security, races, resource
caps") added a per-connection token preamble to `ipc.ServeMCP`. Every
MCP connection had to write `{"token":"<hex>"}\n` before JSON-RPC or it
was rejected:

```go
// ipc/ipc.go (pre-v0.29.3)
if token != "" {
    ar, err := verifyToken(c, token) // reads preamble
    if err != nil { slog.Warn("mcp token verification failed"); return }
}
```

The server side was implemented and enforced. The client side was not.
The MCP client lives inside the agent container and connects over a
socat stdio bridge:

```json
// ant/src/index.ts:451, container/runner.go:728
"arizuko": {
  "command": "socat",
  "args": ["STDIO", "UNIX-CONNECT:/workspace/ipc/gated.sock"]
}
```

socat pipes stdio straight to the socket. It has no hook for emitting a
preamble first. The MCP SDK opens the connection, immediately writes
`initialize`, the server expects a token line, parses JSON-RPC as a
token, fails, closes the socket.

Every MCP tool call went through this path. Every one was rejected.
Agents ran normally for anything that didn't need MCP (the Result path
goes through stdout, not MCP), so the bug stayed invisible except when
users asked for memory. Gateway logged `mcp token verification failed`
on every spawn; nobody was reading that channel.

## Why the hardening was wrong

The preamble was layered as "defense in depth" on top of a socket that
already had filesystem permissions and mount isolation. The intent was
reasonable (belt and suspenders) but three things went wrong:

1. **Untested end-to-end.** Nothing in the repo verified that an MCP
   tool call from inside an agent container actually succeeded. Server
   side had unit tests; no integration test ever dialed the socket
   through the ant runtime with a real tool invocation.
2. **Threat model unstated.** The audit commit message said "auth
   hardening" without naming what the token defended against that
   filesystem perms + mount isolation didn't. In retrospect, the answer
   was "rogue sidecar in the same group" (agent and sidecars share
   uid 1000 and the same `/workspace/ipc/` mount) — a real but narrow
   threat. That intent needed to be the first line of the PR.
3. **Client side orphaned.** The server expected a preamble; the
   client was `socat`. No code change to ant wired the `ARIZUKO_MCP_TOKEN`
   env var into a preamble writer. The token was generated, stamped
   into the container's env, then ignored.

Adding server-side verification with no client-side implementation
guarantees a silent outage on first deploy. The fault was not "bad
cryptography" — the token plumbing was fine — it was shipping a
half-built protocol.

## What the boundary actually is

arizuko's real isolation comes from **per-group bind mounts**, not from
anything on the socket itself.

```go
// container/runner.go buildMounts
ipcDir, _ := folders.IpcPath(in.Folder)  // validated, per-group
m = append(m, volumeMount{
    Host:      hp(cfg, ipcDir),
    Container: "/workspace/ipc",
})
```

`folders.IpcPath(folder)` resolves to `<IpcDir>/<folder>` and refuses
`..`, backslashes, empty segments, and reserved names (`share`, `*`,
`**`). Each agent container sees only its own group's `ipcDir` — other
groups' sockets are unreachable through the mount. That's the boundary.

Socket perms (`chmod 0660`, `chown <uid>`) are not the boundary. Host
root always wins; socket perms can't stop that. The container uid
(1000 = `node`) matches gated's uid (1000) because the gated service
itself runs as 1000 in a container, so the socket naturally ends up
`1000:1000 0660`.

## What replaced the token (v0.29.4)

A kernel-attested `SO_PEERCRED` check on every accepted connection.
The kernel hands us the connecting process's `{pid, uid, gid}`; we
reject anything whose uid isn't the expected container uid.

```go
// ipc/ipc.go v0.29.4
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
- **Kernel-attested.** Not spoofable by the peer; `SO_PEERCRED` is set
  by the kernel at `connect()`.
- **Cheap.** One syscall per connection.
- **Fails closed by default** in production (expectedUID = 1000). Set
  to `-1` for unit tests where the caller runs in-process.

It is still not the boundary. Mount isolation is. Peer-uid check is a
sanity gate: in production both gated and agent run as uid 1000, so
any connecting process must also be uid 1000. Host processes as other
uids are rejected. It does not distinguish agent from sidecar (same
uid); that's covered by mount scoping and process model, not by this
check.

Unit tests (`TestServeMCP_PeerCredAcceptsMatchingUID`,
`TestServeMCP_PeerCredRejectsWrongUID`) confirm both branches.

## Audit conclusions

| #   | Finding                                                                                                                                | Status              |
| --- | -------------------------------------------------------------------------------------------------------------------------------------- | ------------------- |
| 1   | Token preamble broke every MCP call in production                                                                                      | fixed v0.29.3       |
| 2   | No integration test covered an agent→gateway MCP roundtrip                                                                             | still open          |
| 3   | Per-group mount isolation is the real security boundary; document it                                                                   | done (this page)    |
| 4   | `SO_PEERCRED` is a sufficient sanity check; no token needed                                                                            | implemented v0.29.4 |
| 5   | Dead code (`GenerateRuntimeToken`, `verifyToken`, `McpToken`, env stamp) left behind after the fix                                     | removed v0.29.4     |
| 6   | No ownership of the "security sweep" pattern — a broad audit commit shouldn't ship protocol changes without client+server+e2e together | process note        |

## Lessons

- **Shipping one half of a protocol is worse than shipping neither.**
  The token added failure modes without adding safety, because the
  client side wasn't there. If an `e2e_mcp_roundtrip_test` had existed,
  the bug would have been caught at commit time.

- **Don't layer auth onto a transport that's already scoped.** arizuko's
  socket is scoped by mount. Layering a preamble on top made two
  independent ways to fail instead of one clearly documented one.

- **Name the threat in the commit message.** "Auth hardening" is not a
  threat; "prevent same-uid host processes from speaking MCP to a
  group's gated socket" is. If the commit had stated the threat,
  reviewers would have asked "how does the client send the preamble?"
  before merge.

- **Prefer kernel primitives to handshakes.** `SO_PEERCRED` costs one
  syscall, requires no protocol change, is not spoofable. Custom
  handshakes have exactly the failure mode we saw: client and server
  drift out of sync, the difference is invisible until it matters.

- **Observability is not logs nobody reads.** The gateway was logging
  "mcp token verification failed" hundreds of times per day for two
  days. Add a healthcheck that counts MCP-reject events and alerts if
  the count is nonzero; a single successful agent boot should imply a
  successful MCP handshake.

## Follow-ups

- Add an e2e test that spawns a container, calls `get_history` over
  MCP, asserts a non-empty result. (tracked in `bugs.md`)
- Consider a smoke-test endpoint on gated: `/health/mcp` that opens
  its own unix-client to `gated.sock`, calls `ping`, and returns 200
  only if the tool responds within a deadline.
- Consider per-group gid on the socket (`chown :arizuko-<folder>-mcp`)
  if we ever want multiple host processes to speak MCP without sharing
  the container uid. Not required today.
