---
status: shipped
---

# Chat-Bound Sessions

One container per folder. Strictly serial within a folder, parallel
across folders. All I/O via IPC files. File deletion = acknowledgment.

## IPC directory encoding

Folder path encoded: `/` → `-`, `-` → `--`.

```
root           -> /ipc/root/
atlas/support  -> /ipc/atlas-support/
atlas-v2       -> /ipc/atlas--v2/
```

## Delivery guarantees

- File deleted = processed.
- File exists = not processed (retries next run).
- Crash = partial (deleted = delivered; remaining retry).
- No duplicates: each message processed at most once per run.

## Parallelism

| Scenario                         | Behavior                          |
| -------------------------------- | --------------------------------- |
| JID1 → folder A, JID2 → folder A | Serial. JID2 waits.               |
| JID1 → folder A, JID2 → folder B | Parallel. Separate containers.    |
| JID1 → folder A, JID1 → folder B | Parallel. Same JID, diff folders. |
