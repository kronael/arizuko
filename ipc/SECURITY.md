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
ipcDir, _ := folders.IpcPath(in.Folder)  // validated, per-group
m = append(m, volumeMount{
    Host:      hp(cfg, ipcDir),
    Container: "/workspace/ipc",
})
```

`folders.IpcPath(folder)` rejects `..`, backslashes, empty segments,
reserved names (`share`, `*`, `**`). Other groups' `ipcDir` are
unreachable through the mount — there's no path inside the container
that resolves to them.

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

Record of the audit and fix for this channel. A 2-day production
incident in which agents silently lost access to every gateway tool
(`get_history`, `get_facts`, `search_memories`, `send_file`,
`reset_session`, cross-group delegation, …) while appearing healthy.
The symptom was agents replying "nemám záznam", "no context" —
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
