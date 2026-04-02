---
status: draft
---

## <!-- trimmed 2026-03-15: TS removed, rich facts only -->

## status: shipped

# Chat-Bound Sessions

One container per folder, strictly serial within folder, parallel
across folders. All I/O via IPC files. File deletion = acknowledgment.

## IPC Directory Encoding

Folder path encoded: `/` -> `-`, `-` -> `--`.

```
root           -> /ipc/root/
atlas/support  -> /ipc/atlas-support/
atlas-v2       -> /ipc/atlas--v2/
```

## Delivery Guarantees

- **File deleted = processed.** Container deletes after success.
- **File exists = not processed.** Stays pending, retried next run.
- **Crash = partial.** Deleted files delivered; remaining retry.
- **No duplicates.** Each message processed at most once per run.

## Parallelism

| Scenario                           | Behavior                          |
| ---------------------------------- | --------------------------------- |
| JID1 -> folder A, JID2 -> folder A | Serial. JID2 waits.               |
| JID1 -> folder A, JID2 -> folder B | Parallel. Separate containers.    |
| JID1 -> folder A, JID1 -> folder B | Parallel. Same JID, diff folders. |
