# davd

davd serves group workspaces through WebDAV and a browser file UI. The image wraps the upstream `dufs` binary in Alpine.
It owns no store entities, identities, sessions, or application configuration.

[proxyd](../proxyd/README.md) enforces authentication, group scope, and write permissions before requests reach davd.

```text
browser or WebDAV client ──► proxyd /dav/ ──► davd:8080 /data
                                               │
                                               ▼
                                      DATA_DIR/groups on disk
```

## Interfaces and trust

| Interface                                             | Behavior                                                                  |
| ----------------------------------------------------- | ------------------------------------------------------------------------- |
| `/` and workspace paths                               | dufs directory UI and WebDAV methods on `/data`.                          |
| `/dav/` on proxyd                                     | User-authenticated entry; proxyd strips the prefix and confines the path. |
| `/health`, `/openapi.json`, `/metrics`, `/dash/davd/` | Not mounted by arizuko. Health probes use `/`.                            |

There are no resreg resources, MCP tools, IPC sockets, or calls to routd or authd.
Compose declares `depends_on: [routd]`; davd does not call routd.
The backend has no arizuko identity checks. Keep access behind proxyd.
`--allow-all` permits writes at the dufs layer; proxyd's `davAllow` guard decides which client writes may pass.

## State and configuration

Compose mounts the instance's `groups` directory read/write at `/data`.
Workspace files are davd's only persistent state. `store.StoreReach` has no davd entry because davd never opens a store.

| Setting          | Default                         | Meaning                                                                   |
| ---------------- | ------------------------------- | ------------------------------------------------------------------------- |
| dufs command     | `--port 8080 --allow-all /data` | Generated command; davd reads no arizuko environment variables.           |
| `WEBDAV_ENABLED` | unset                           | Compose input that enables the profile and `/dav/` route.                 |
| `DAV_PORT`       | unset                           | Compose input for an optional `127.0.0.1:<DAV_PORT>:8080` mapping.        |
| `DATA_DIR`       | instance configuration          | Compose input selecting the host `groups` mount; not a davd process read. |

Edit instance configuration and regenerate compose through the CLI. See [installation](../INSTALL.md).
The Dockerfile uses `sigoden/dufs:latest` and `alpine:3.20`; the repository does not pin the dufs binary digest.

## Health and failure diagnosis

Docker probes `GET /` every 30 s, with a 3 s wget timeout, 5 s healthcheck timeout, and 10 s start period.
Three failed probes mark the container unhealthy. The generated restart policy is `on-failure`.
An unhealthy status alone does not restart a running container.

A store or router outage does not stop an established dufs server from serving files.
A missing, unreadable, full, or read-only mount causes file operations to fail at the backend.
There is no arizuko retry queue or scheduler in this image.
Inspect container health, dufs stderr, and proxyd access/auth failures when `/dav/` fails.
There is no `obs.Setup`, OTLP export, or Prometheus instrumentation in the wrapper.

## Limits and source evidence

The repository contains a Dockerfile and compose invocation, not dufs source or WebDAV integration tests.
The health probe establishes index reachability; it does not test authenticated writes, group confinement, or disk capacity.
Live upstream WebDAV behavior is not verified by local documentation checks.

Sources: [image](Dockerfile), [compose command](../compose/compose.go 'davdService'),
[proxy enforcement](../proxyd/main.go 'davAllow').
