# queue

Per-group concurrency for container runs.

## Purpose

Serializes agent invocations per group: one container per group at a
time, global cap `MAX_CONCURRENT_CONTAINERS`. Steers mid-flight messages
into the running container via IPC input files instead of spawning a new
one. Circuit-breaks after 3 consecutive failures.

## Public API

- `New(maxConcurrent int, ipcDir string) *GroupQueue`
- `(*GroupQueue).SetProcessMessagesFn`, `SetHasPendingFn`, `SetNotifyErrorFn` — wired by gateway
- `(*GroupQueue).EnqueueMessageCheck(groupJid)` — signal-only; queue calls back into gateway
- `(*GroupQueue).RegisterProcess(groupJid, containerName, groupFolder)` — container started
- `(*GroupQueue).SendMessages(groupJid, texts) bool` — steer into running container (writes one IPC input file per message)
- `(*GroupQueue).ActiveCount() int`
- `(*GroupQueue).StopProcess(jid) bool`
- `(*GroupQueue).Shutdown()`

## Dependencies

- `groupfolder` (IPC paths)

## Files

- `queue.go` — `GroupQueue`, circuit breaker, IPC input file writer

## Related docs

- `ARCHITECTURE.md` (Queue)
