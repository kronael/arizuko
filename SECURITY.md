# Security

arizuko's security model and boundaries. Daemons with non-trivial
surface ship their own `SECURITY.md` next to the source; this file is
the index and cross-cutting model.

## Model

Three isolation axes:

1. **Group isolation** — per-group bind mounts; no path inside one
   group's container resolves to another group's files or sockets.
2. **User isolation** — users reach groups only via `acl` allow rows
   (directly or through `acl_membership`). OAuth → JWT at the proxy
   edge; `auth.Authorize` enforces.
3. **Daemon isolation** — adapters reach gated only over the internal
   docker network with a shared `CHANNEL_SECRET` bearer token.

### The group is the tenant boundary

The isolation unit in arizuko is the **group**, not the turn. Each
group runs in its own Docker container with its own filesystem, its
own network namespace, its own DB view, and its own egress allowlist.
`solo/inbox` and `corp/eng` cannot see each other's files, sockets,
network state, or messages — and this is structural, enforced by
docker + bind mounts + MCP folder scoping, not by policy inside the
agent.

What's isolated per group:

- **Filesystem** — only `groups/<folder>/` mounts at `/home/node/`;
  `groupfolder.Resolver` rejects `..` and reserved paths
  (`groupfolder/folder.go`, `container/runner.go` `buildMounts`).
- **Network namespace** — agent container attaches to a Docker
  `internal: true` network with no default route; egress passes
  through `crackbox` against the per-folder allowlist
  (`container/egress.go`, `crackbox/`).
- **DB view** — the MCP socket at `ipc/<folder>/gated.sock` carries
  the group's identity (`SO_PEERCRED` + socket-path tier); every
  tool call resolves through `auth.Authorize` against that folder,
  not the caller's claim (`ipc/ipc.go`, `auth/middleware.go`).
- **Secrets** — only folder-scoped secrets reach the container env;
  per-user tokens stay on the host and resolve at tool-call time
  through the broker (`container/runner.go` `resolveSpawnEnv`,
  spec 9/11).

The threat the model defends against is a malicious agent in group A
trying to reach group B's data, files, or network. Separate containers
on separate networks with separate DB views close that path
structurally. The agent runs with `bypassPermissions` inside its own
container by design — the boundary is the mount set and the network
namespace, not what the agent decides to do with its shell.

Per-turn (or per-spawn) container freshness solves a different
problem: a compromised turn affecting the next turn in the **same**
group. arizuko's group containers are long-lived precisely because
the conversation state, diary, skills, and session jsonl live in
`/home/node/` and need to persist across turns. Per-turn isolation
would destroy that context; per-group isolation is what makes the
cross-tenant threat go away.

## Boundaries

| Boundary            | Mechanism                                                                                                                        | Location                                                                                          |
| ------------------- | -------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------- |
| Group filesystem    | Per-group bind mount; `Resolver.GroupPath` rejects `..`, `\`, reserved                                                           | `groupfolder/folder.go`, `container/runner.go` (`buildMounts`)                                    |
| Group MCP socket    | Per-group mount of `ipcDir` → `/workspace/ipc`; `Resolver.IpcPath` rejects                                                       | `container/runner.go` (`buildMounts`)                                                             |
| MCP peer identity   | `SO_PEERCRED` peer-uid check on each accepted conn                                                                               | `ipc/ipc.go` (`ServeMCP`), see `ipc/SECURITY.md`                                                  |
| Agent exec scope    | `bypassPermissions` inside container; mounts scoped to the group                                                                 | `ant/src/index.ts`, `container/runner.go`                                                         |
| Additional mounts   | `ValidateAdditionalMounts` + `ValidateFilePath` blocklist/symlink guard                                                          | `mountsec/`                                                                                       |
| Web routes          | `/pub/*` + `/health` public; `/slink/*` 301→`/chat/<token>/`; `/chat/*` + `/hook/*` token; rest JWT                              | `proxyd/main.go`                                                                                  |
| Chat-MCP            | `/chat/<token>/mcp` — token IS the auth; possession = group membership                                                           | `webd/chat_mcp.go` (`handleChatMCP`)                                                              |
| WebDAV write-block  | `.env` / `*.pem` / `.git` write-blocked; `<group>/logs/` read-only                                                               | `proxyd/main.go` (`davAllow`)                                                                     |
| Chat identity relay | proxyd signs `X-Folder` via HMAC with `X-Chat-Token`; webd verifies                                                              | `proxyd/main.go`, `auth.SignHMAC`                                                                 |
| Authn               | GitHub / Google / Discord / Telegram OAuth → JWT (1h) + refresh (30d)                                                            | `auth/web.go`, `auth/oauth.go`                                                                    |
| Login throttle      | 5 POST `/auth/login` per IP per 15min, in-memory                                                                                 | `auth/web.go`                                                                                     |
| Authz               | Unified `acl` + `acl_membership` → `auth.Authorize`; `grants.CheckAction` for per-tool param gating                              | `auth/acl.go`, `grants/`                                                                          |
| Channel ingress     | `Authorization: Bearer <CHANNEL_SECRET>`; docker-network only                                                                    | `chanlib/run.go`, `chanlib/chanlib.go` (`Auth`), `api/api.go`                                     |
| Slack webhook       | proxyd forwards `/slack/*` → `slakd:8080` verbatim; `X-Slack-Signature` HMAC over `v0:<ts>:<body>` (signing secret); ±5min skew  | `slakd/bot.go` (verify), `template/services/slakd.toml` (route), `slakd/README.md` § Threat model |
| Email sender auth   | DMARC via pinned `Authentication-Results` authserv-id + operator allowlist; fail → `verb=untrusted`, never promoted to `mention` | `emaid/imap.go`, spec 8/17                                                                        |
| Mention promotion   | Gateway-side `verb=mention` rewrite when parent is bot-authored; one renderer across all adapters, untrusted verbs never promote | `gateway/gateway.go`, spec 6/J                                                                    |
| Secrets at rest     | AES-256-GCM encrypted (v1: prefix, key=SHA-256(AUTH_SECRET)); plaintext rows purged on startup                                   | `store/secrets.go`, migration `0034-secrets.sql`                                                  |
| Secret injection    | Folder/user secrets env injection planned (spec 6/Y); today only API keys from host env injected                                 | `container/runner.go` (`resolveSpawnEnv`)                                                         |
| Onboarding rate cap | Per-gate daily limit from `onboarding_gates` table                                                                               | `onbod/main.go` (`admitFromQueue`)                                                                |
| Network egress      | Default-deny; per-folder allowlist enforced by forward proxy                                                                     | `crackbox/`, `store/network.go`, `container/egress.go`                                            |
| DNS filter          | UDP/53 listener returns NXDOMAIN for non-allowlisted hostnames; REFUSED for ANY                                                  | `crackbox/pkg/dns/`, `specs/9/15-crackbox-dns-filter.md`                                          |

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

The shared secret is `PROXYD_HMAC_SECRET` in `.env`. It must be set
and **identical** in both `proxyd` and `webd` (and `onbod`). The
compose generator propagates it from `.env` to each service's scoped
`env/<daemon>.env`. If unset, proxyd generates an ephemeral secret
per run — webd will then reject all signed headers, breaking ant link
SSE auth and authenticated web chat.

Crypto lives in `auth/hmac.go` (`SignHMAC`, `VerifyUserSig`,
`UserSigMessage`) and is shared by both signer and verifiers. Never
inline a `VerifyUserSig` call in handler code — go through the
middleware. The single exception is in `webd/chat.go` where signed
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
│  │  teled   discd   slakd   mastd   whapd   ...        │ │
│  │                                                     │ │
│  │  ┌─ agent container (per-group, partially trusted) ┐│ │
│  │  │  claude code + ant/ + skills                    ││ │
│  │  │  runs arbitrary Bash/Edit/WebFetch              ││ │
│  │  │  scoped to /workspace/ipc + group mounts        ││ │
│  │  └─────────────────────────────────────────────────┘│ │
│  └─────────────────────────────────────────────────────┘ │
│                                                          │
│  ┌─ external channels (untrusted) ─────────────────────┐ │
│  │  telegram, discord, whatsapp, web chat, email        │ │
│  └─────────────────────────────────────────────────────┘ │
└──────────────────────────────────────────────────────────┘
```

## Network egress isolation

When `CRACKBOX_ADMIN_API` is set and `crackbox:latest` is running,
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

- Only `ANTHROPIC_API_KEY` and `CLAUDE_CODE_OAUTH_TOKEN` from host env
  are injected into the container today. The allowlist restricts _where_
  the agent can reach; it does not prevent leaking secrets to an allowed
  domain.
- Spec 6/Y (tool-level secret broker) is **planned, not yet shipped**:
  the broker middleware (`ipc/ipc.go` `injectSecretsAdapter`) will resolve
  `user(caller.Sub)` then `folder(caller.Folder)` at tool-call time;
  audit rows will flow into `secret_use_log`; per-user secrets will
  never enter container env. The connector path (`mcp_connector` TOML +
  per-call subprocess) and `/dash/me/secrets` user surface are part of
  the same spec. OAuth-bound connector flows deferred to spec 9/14.
- IPv6 is not redirected by the entrypoint script.

**DNS filter** (`crackbox/pkg/dns/`,
`specs/9/15-crackbox-dns-filter.md`). The crackbox-side UDP/53
listener is shipped: gated allowlisted hostnames forward to the
upstream resolver; denied hostnames return NXDOMAIN; `QTYPE=ANY`
returns REFUSED; malformed/multi-question packets drop silently.
**Pending:** arizuko-side wiring (passing `--dns <crackbox-ip>` from
`container.Run` to `docker create`) under
`specs/9/10-crackbox-arizuko.md`; today agent containers still use
the default Docker resolver, so the DNS path is additive defense
rather than the primary gate. The HTTP/CONNECT 403 in
`crackbox/pkg/proxy/` remains the enforced path.

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
