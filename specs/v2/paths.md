# Path translation spec (v2) — open

## Problem

The gateway runs inside a Docker container. It spawns child agent containers
via the host Docker daemon (DinD). Paths that exist inside the gateway
container must be translated to host-side paths for child container mounts
and for reading files the agent wrote.

Currently this is solved piecemeal with multiple overlapping mechanisms:

- `hostPath(localPath)` in `container-runner.ts` — replaces `GATEWAY_ROOT`
  with `HOST_PROJECT_ROOT_PATH` for volume mounts
- `HOST_GROUPS_DIR` in `config.ts` — host-side equivalent of `GROUPS_DIR`,
  used in `ipc.ts` for file path resolution
- `HOST_APP_DIR` in `config.ts` — host-side equivalent of app dir, used for
  the `/workspace/self` and agent-runner mounts
- `unlink` in `ipc.ts` uses `DATA_DIR` (gateway-internal path) which only
  works because the gateway container mounts the same dir

Each fix was added reactively. The result is three separate translation
mechanisms with different naming conventions and different failure modes.

## Known issues

### ipc.ts unlink race

`drainGroupMessages` deletes IPC files with `fs.unlinkSync(filePath)` where
`filePath` uses gateway-internal `DATA_DIR`. This works because the gateway
container mounts the data dir. But inotify fires multiple times per write
event, causing duplicate processing attempts and `ENOENT` on the second
unlink. Logged as `ERROR` but harmless.

**Fix**: wrap `fs.unlinkSync` in a try/catch that ignores `ENOENT`, or check
`fs.existsSync` before processing (avoids duplicate delivery too).

### Fragile path translation

`hostPath()` does a string replace of `GATEWAY_ROOT` with
`HOST_PROJECT_ROOT_PATH`. If any path is not under `GATEWAY_ROOT` (e.g. a
path under `APP_DIR` that differs from `GATEWAY_ROOT`), the replace is a
no-op and the wrong path is silently passed to docker.

`HOST_GROUPS_DIR` is an explicit known-good path — no string replacement.
This pattern is safer.

### Missing HOST_DATA_DIR export

`HOST_DATA_DIR` is set by the `kanipi` script and available as
`HOST_PROJECT_ROOT_PATH` in config. But there's no `HOST_DATA_DIR` export,
so callers must compute `path.join(HOST_PROJECT_ROOT_PATH, 'data', ...)`.

## Proposed design

Replace all path translation with explicit host-path exports computed once
at startup from known env vars:

```ts
// config.ts
export const HOST_DATA_DIR = path.resolve(HOST_PROJECT_ROOT, 'data');
export const HOST_GROUPS_DIR = path.resolve(HOST_PROJECT_ROOT, 'groups');
// HOST_APP_DIR already exists
```

Replace `hostPath()` string replacement with explicit per-call construction:

```ts
// container-runner.ts — instead of hostPath(groupDir):
const hostGroupDir = path.join(HOST_GROUPS_DIR, group.folder);

// for session dirs:
const hostSessionDir = path.join(
  HOST_DATA_DIR,
  'sessions',
  group.folder,
  '.claude',
);

// for ipc dir:
const hostIpcDir = path.join(HOST_DATA_DIR, 'ipc', group.folder);
```

Delete `hostPath()` and `GATEWAY_ROOT` entirely.

## Fix priority

1. `ipc.ts` unlink ENOENT — noisy logs, easy fix (ignore ENOENT on unlink)
2. `hostPath()` elimination — correctness, medium effort
3. `HOST_DATA_DIR` export — prerequisite for (2)

## Related

- `specs/v1/ipc-signal.md` — shipped
- `container-runner.ts:hostPath()` — the function to eliminate
