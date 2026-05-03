# Security

arizuko's security model and boundaries. Daemons with non-trivial
surface ship their own `SECURITY.md` next to the source; this file is
the index and cross-cutting model.

## Model

Three isolation axes:

1. **Group isolation** — per-group bind mounts; no path inside one
   group's container resolves to another group's files or sockets.
2. **User isolation** — users reach groups only via `user_groups` rows.
   OAuth → JWT at the proxy edge; `auth.MatchGroups` enforces.
3. **Daemon isolation** — adapters reach gated only over the internal
   docker network with a shared `CHANNEL_SECRET` bearer token.

## Boundaries

| Boundary             | Mechanism                                                                  | Location                                                       |
| -------------------- | -------------------------------------------------------------------------- | -------------------------------------------------------------- |
| Group filesystem     | Per-group bind mount; `Resolver.GroupPath` rejects `..`, `\`, reserved     | `groupfolder/folder.go`, `container/runner.go` (`buildMounts`) |
| Group MCP socket     | Per-group mount of `ipcDir` → `/workspace/ipc`; `Resolver.IpcPath` rejects | `container/runner.go` (`buildMounts`)                          |
| MCP peer identity    | `SO_PEERCRED` peer-uid check on each accepted conn                         | `ipc/ipc.go` (`ServeMCP`), see `ipc/SECURITY.md`               |
| Agent exec scope     | `bypassPermissions` inside container; mounts scoped to the group           | `ant/src/index.ts`, `container/runner.go`                      |
| Additional mounts    | `ValidateAdditionalMounts` + `ValidateFilePath` blocklist/symlink guard    | `mountsec/`                                                    |
| Web routes           | `/pub/*` + `/health` public; `/slink/*` token + 10/min/IP; rest JWT        | `proxyd/main.go`                                               |
| Slink-MCP            | `/slink/<token>/mcp` — token IS the auth; possession = group membership    | `webd/slink_mcp.go` (`handleSlinkMCP`)                         |
| WebDAV write-block   | `.env` / `*.pem` / `.git` write-blocked; `<group>/logs/` read-only         | `proxyd/main.go` (`davAllow`)                                  |
| Slink identity relay | proxyd signs `X-Folder` via HMAC; webd verifies                            | `proxyd/main.go`, `auth.SignHMAC`                              |
| Authn                | GitHub / Google / Discord / Telegram OAuth → JWT (1h) + refresh (30d)      | `auth/web.go`, `auth/oauth.go`                                 |
| Login throttle       | 5 POST `/auth/login` per IP per 15min, in-memory                           | `auth/web.go`                                                  |
| Authz                | `user_groups` ACL → `auth.MatchGroups`; grants engine for agent tools      | `auth/acl.go`, `grants/`                                       |
| Channel ingress      | `Authorization: Bearer <CHANNEL_SECRET>`; docker-network only              | `chanlib/run.go`, `chanlib/chanlib.go` (`Auth`), `api/api.go`  |
| Secrets at rest      | AES-GCM(`AUTH_SECRET`) over folder/user-scoped k=v rows                    | `store/secrets.go`, migration `0034-secrets.sql`               |
| Secret injection     | Resolved at container spawn; folder always, user only in 1:1 chats         | `container/runner.go` (`resolveSpawnEnv`)                      |
| Onboarding rate cap  | Per-gate daily limit from `onboarding_gates` table                         | `onbod/main.go` (`admitFromQueue`)                             |
| Network egress       | Default-deny; per-folder allowlist enforced by forward proxy               | `crackbox/`, `store/network.go`, `container/egress.go`         |

Anything not in this table is not a security boundary. In particular:
socket filesystem permissions alone do not separate containers, and
host root always wins — arizuko does not defend against the host
operator. The agent container runs with `bypassPermissions`; the
boundary is the mount set, not the tool policy.

## Identity header trust

`proxyd` is the **sole signer** of identity headers
(`X-User-Sub`, `X-User-Name`, `X-User-Groups`, `X-User-Sig`).
Every other HTTP-receiving backend MUST verify the signature via the
centralized middleware in `auth/middleware.go`:

- `auth.RequireSigned(secret)` — strict, redirect-on-fail. Use on
  always-authed backends (e.g. `webd/server.go:44` constructs it once
  and stamps it on every private route).
- `auth.StripUnsigned(secret)` — lenient, scrub-spoofed-and-continue.
  Use on backends mixing public + authed flows (e.g. `onbod/main.go:81`
  protects `/onboard` and `/invite/{token}` so unauthenticated landings
  still work but a forged `X-User-Sub` never reaches a handler).

Crypto lives in `auth/hmac.go` (`SignHMAC`, `VerifyUserSig`,
`UserSigMessage`) and is shared by both signer and verifiers. Never
inline a `VerifyUserSig` call in handler code — go through the
middleware. The single exception is in `webd/slink.go` where signed
identity is one of two acceptable identities (the other being
anonymous-from-trusted-IP); even there, the call is a boolean
"is this user known?" check, not an authentication gate.

## Trust zones

```
┌─ host (trusted) ─────────────────────────────────────────┐
│                                                          │
│  ┌─ arizuko_<instance> (docker-compose, trusted) ──────┐ │
│  │                                                     │ │
│  │  gated   onbod   dashd   webd   proxyd   timed      │ │
│  │  teled   discd   mastd   whapd  ...                 │ │
│  │                                                     │ │
│  │  ┌─ agent container (per-group, partially trusted) ┐│ │
│  │  │  claude code + ant/ + skills                    ││ │
│  │  │  runs arbitrary Bash/Edit/WebFetch              ││ │
│  │  │  scoped to /workspace/ipc + group mounts        ││ │
│  │  └─────────────────────────────────────────────────┘│ │
│  └─────────────────────────────────────────────────────┘ │
│                                                          │
│  ┌─ external channels (untrusted) ─────────────────────┐ │
│  │  telegram, discord, whatsapp, web slink, email      │ │
│  └─────────────────────────────────────────────────────┘ │
└──────────────────────────────────────────────────────────┘
```

## Network egress isolation

When `EGRESS_ISOLATION=true` and `crackbox:latest` is running,
agent containers attach to a Docker `internal: true` network with no
default route to the internet. The only path out is via `crackbox`,
which runs on both the internal network and the project default bridge.

- Agent containers receive `HTTPS_PROXY=http://crackbox:3128` at spawn
  plus `NODE_OPTIONS=--require=/app/proxy-shim.js` so Node's built-in
  fetch honors the proxy (curl/wget/pip/go/npm honor it natively).
  crackbox forwards HTTP and CONNECT-tunnels HTTPS without decrypting.
- Non-cooperating clients fail closed: the internal Docker network
  has no default route, so a client that ignores `HTTPS_PROXY` cannot
  reach the internet at all.
- Per-source-IP allowlist populated by gated at container spawn from
  `store.ResolveAllowlist(folder)` (folder ancestry walk + dedupe).
- Default seed: `anthropic.com`, `api.anthropic.com`. Operators add
  rules via `arizuko network <instance> allow <folder> <target>`.
- Unknown source IP or unmatched host → connection closed silently.

Caveats:

- Agent secrets (per spec 5/32) are still injected as env into the
  container. The allowlist restricts _where_ the agent can reach;
  it does not prevent leaking secrets to an allowed domain.
- Spec 11 (proxy-side placeholder injection) is deferred. Adding it
  later would require terminating TLS for selected hosts; the current
  forward-proxy design is intentionally MITM-free.
- IPv6 is not redirected by the entrypoint script.

## Per-daemon docs

- [`ipc/SECURITY.md`](ipc/SECURITY.md) — MCP channel, `SO_PEERCRED`
  check, per-group mount isolation, incident history

Other daemons do not have a dedicated file yet; guarantees are in the
table above. Add one next to the daemon when its threat model outgrows
a row.

## Incident log

**2026-04-17 → 2026-04-19 — MCP token preamble outage.** Commit
`2774394` added a server-side token preamble to `ipc.ServeMCP` with no
matching client (ant's socat bridge cannot write a preamble). Every MCP
call rejected for ~48h; agents appeared amnesic. Fixed v0.29.3 (disable)
and replaced v0.29.4 with `SO_PEERCRED`. Full writeup:
[`ipc/SECURITY.md`](ipc/SECURITY.md).

## Reporting

Log to `bugs.md` with reproducer and severity. No public channel —
arizuko is single-operator per instance.
