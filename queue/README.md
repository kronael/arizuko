# queue

Per-group concurrency for agent runs. [routd](../routd/README.md) owns turn
handling and provider pauses; [runed](../runed/README.md#admission-is-claim-or-reject)
owns guest admission.

## One turn holds each folder

`GroupQueue` serializes invocations per folder and enforces the global cap
`MAX_CONCURRENT_CONTAINERS`. Chats that resolve to the same folder wait for
the active turn to end. The parked callback reports waiting presence.

## Public API

- `New(maxConcurrent int) *GroupQueue`
- `(*GroupQueue).SetProcessMessagesFn`, `SetFolderForJidFn`, `SetParkedFn` — callbacks wired by routd
- `(*GroupQueue).EnqueueMessageCheck(groupJid)` — signal-only; the queue calls routd
- `(*GroupQueue).ActiveCount() int`
- `(*GroupQueue).Shutdown()`

## Files

- `queue.go` — folder exclusion, concurrency and dependency-outage logging
