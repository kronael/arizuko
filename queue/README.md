# queue

Per-group concurrency for container runs.

## Purpose

Serializes agent invocations per group: one live turn per group at a
time, global cap `MAX_CONCURRENT_CONTAINERS`. Steers mid-flight messages
into the live turn through its IPC input files rather than starting a
second guest. Circuit-breaks after 3 consecutive failures.

## Two breakers count different things

routd's `GroupQueue` counts consecutive failures per chat JID and opens at 3
(`circuitBreakerThreshold`, `queue.go`). The next inbound for that chat
resets it. At open, routd deletes the chat's errored rows and the folder's
session (`routd/loop.go` `onCircuitBreakerOpen`), so the next message gets a
clean run. runed's Manager counts failures per folder (`runed/README.md`
§ Admission is claim or reject). A transport failure to runed charges
neither breaker, and a `busy` answer charges neither. The routd dashboard's
retry clears a chat's errored rows; there is no separate reset verb.

## Public API

- `New(maxConcurrent int, ipcDir string) *GroupQueue`
- `(*GroupQueue).SetProcessMessagesFn`, `SetFolderForJidFn` — wired by routd (`routd/loop.go:235-236`)
- `(*GroupQueue).EnqueueMessageCheck(groupJid)` — signal-only; queue calls back into routd
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

- `runed/README.md` § Admission is claim or reject — the guest cap and steer
