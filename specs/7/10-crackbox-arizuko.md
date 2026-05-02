---
status: shipped
shipped: 2026-04-29
depends: 9-crackbox-standalone
---

# Crackbox in arizuko — current consumer + sandd transition

> Today: `gated` directly spawns Docker + POSTs `/v1/register` to
> egred. Next: `gated` gains a Backend interface and imports
> [`crackbox/pkg/host/`](../6/12-crackbox-sandboxing.md) for the KVM
> backend. Sandd extraction ([8/c](../8/c-sandd.md)) deferred.
> Egred remains the egress proxy in both paths.

## Status

The egred proxy + arizuko's per-folder allowlist consumer shipped
2026-04-29. This spec describes both the current shipped consumer
and the sandd transition planned for next phase.

## Today's shipped consumer

arizuko runs `egred` (entrypoint `crackbox proxy serve`) as a
Docker compose service. One shared egred per arizuko instance.
The egred container sits on every per-folder Docker network
(attached via `docker network connect --alias crackbox`), so
agents in any folder reach it by short name.

For each agent spawn, arizuko (in `gated`) does:

1. Computes the flat allowlist via `store.ResolveAllowlist(folder)`
   — folder-walk + dedupe.
2. POSTs `/v1/register {ip, id, allowlist}` to the egred admin API
   using `crackbox/pkg/client.Client`.
3. Spawns the agent container on the per-folder network with
   `HTTPS_PROXY=http://crackbox:3128`.
4. On exit, POSTs `/v1/unregister {ip}`.

This works. It also gives `gated` the docker-spawn privilege, which
is the tension that motivates the sandd extraction below.

## Domain vs mechanism boundary

Unchanged from the original spec. arizuko owns domain (folders,
grants, policy composition); the crackbox component owns mechanism
(proxy daemon, matchHost, admin API, future VM lifecycle).

| Owner    | What                                                                                   |
| -------- | -------------------------------------------------------------------------------------- |
| arizuko  | `network_rules` table + migration 0037                                                 |
| arizuko  | `store.ResolveAllowlist` folder-walk and dedupe                                        |
| arizuko  | `arizuko network <instance>` operator CLI                                              |
| arizuko  | `container/egress.go` lifecycle glue (today; moves to sandd)                           |
| arizuko  | `EGRESS_ISOLATION` toggle (deprecated; presence of `CRACKBOX_ADMIN_API` is the switch) |
| arizuko  | Default seed allowlist (`anthropic.com`, `api.anthropic.com`)                          |
| crackbox | The egred proxy daemon                                                                 |
| crackbox | `matchHost` and the domain validators                                                  |
| crackbox | The `/v1/register` etc admin API                                                       |
| crackbox | The `:3128` proxy listener                                                             |

arizuko hands crackbox a flat `(ip, id, []string)`. crackbox never
learns about folders, grants, ancestry, or `messages.db`.

## KVM backend transition (next phase)

`gated` keeps sandbox-spawn ownership. Adds a Backend interface in
`container/runner.go`:

```go
type Backend interface {
    Spawn(Input) (Handle, error)
    Wait(Handle) (ExitCode, error)
    Stop(Handle) error
}
```

Two implementations, selected by env (`SANDBOX_BACKEND=docker|kvm`):

- **`container.DockerBackend`** — current path, refactored behind
  the interface. Zero behavior change.
- **`container.KVMBackend`** — imports `crackbox/pkg/host/`. Spawns
  qemu VM, ensures egred is up, registers VM IP with egred,
  attaches to per-folder network. The library does the privileged
  work; gated just calls into it.

```
gated ─── DockerBackend  → docker run                                (today)
      └── KVMBackend     → crackbox/pkg/host/.Spawn                  (next)
                            ├── spawn qemu
                            ├── ensure egred up (or use external)
                            └── POST /v1/register to egred

both backends ─── POST /v1/register ──→ egred (egress proxy)
```

Privilege impact: gated already mounts `docker.sock` (root-
equivalent). Adding `/dev/kvm` + `CAP_NET_ADMIN` is incremental,
not a new attack surface. The privilege-isolation extraction into a
separate `sandd` daemon ([spec 8/c](../8/c-sandd.md)) is deferred —
ship KVM behind the Backend interface first; revisit the daemon
split when there's a concrete symptom that needs it.

Migration is mechanical:

1. `container/runner.go` extracts current Docker-spawn into
   `container.DockerBackend`. `container.Run` becomes
   `backend.Spawn(Input)` selected by env.
2. `container/egress.go` (the per-spawn register/unregister) stays
   where it is; both backends call it the same way.
3. `container.KVMBackend` lands as new code that imports
   `crackbox/pkg/host/` (which lands per spec 8/a).
4. `arizuko/cmd/arizuko/network.go`, `arizuko/store/network.go`,
   migration 0037 — all stay in arizuko. Folder ancestry is arizuko
   domain.

## No semantic change for end users

Same default-deny behavior. `arizuko network <inst> allow <folder>
<host>` still works. Per-folder allowlists still resolve via
folder-walk. KVM backend changes the runtime artifact (qemu instead
of docker container) but not the observable user experience.

## Out of scope

- Spec 6/11 placeholder injection.
- MCP tools (`request_network`, `list_network_rules`) — CLI only.
- Per-user network rules (per-folder only).
- Traffic logging and audit.
- Response scanning.

## Acceptance

For today's shipped state:

- Agent spawn on krons triggers `client.Register` and the journal
  shows `egress registered folder=<f> ip=<ip> rules=<n>`.
- A request to `https://api.anthropic.com` is allowed; a request
  to `https://datadoghq.com` is denied by the proxy.
- `arizuko network krons resolve atlas` returns the seeded list
  plus any folder additions.

For the sandd transition (when shipped):

- `gated` runs as a nonroot user with no `docker.sock` mount.
  `ps aux | grep docker.sock` shows only `sandd`.
- All existing smoke tests pass.
- Agent spawn behavior is byte-identical: same image, same env,
  same mounts, same network attach.
