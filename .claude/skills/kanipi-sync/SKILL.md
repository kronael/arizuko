---
name: kanipi-sync
description: Check kanipi for new changes since last sync and port relevant ones to arizuko. Use when asked to "sync kanipi", "check kanipi", or "port from kanipi".
---

# Kanipi Sync

Check kanipi for new changes since last sync and merge relevant ones into arizuko.

## Procedure

1. Read `.kanipi-sync-head` for the last synced commit hash
2. Get new commits from kanipi since that hash:
   ```bash
   cd /home/onvos/app/kanipi && git log --oneline <hash>..HEAD
   ```
3. For each new commit, read the diff and classify:
   - **Port**: feature/fix relevant to arizuko → implement in Go
   - **Skip**: TS-specific, already ported, or irrelevant
   - **Note**: new feature to track in delta → update `.kanipi-delta.md`
4. Port relevant changes (implement in Go, matching kanipi behavior)
5. Update `.kanipi-sync-head` with kanipi's current HEAD
6. Update `.kanipi-delta.md` with any new items or status changes
7. Build and test: `make build && make test`

## Key paths

| What | Kanipi | Arizuko |
|------|--------|---------|
| Repo | `/home/onvos/app/kanipi` | `/home/onvos/app/arizuko` |
| Auth | `src/auth.ts` | `auth/` |
| Store | `src/store.ts` | `store/` |
| Gateway | `src/gateway.ts` | `gateway/` |
| Container | `src/container.ts` | `container/` |
| IPC | `src/ipc.ts` | `ipc/` |
| Config | `src/config.ts` | `core/config.go` |
| Sync head | — | `.kanipi-sync-head` |
| Delta | — | `.kanipi-delta.md` |

## Rules

- NEVER blindly copy TS code — rewrite in idiomatic Go
- Match kanipi's shipped behavior, not its implementation
- Skip TS-specific patterns (class inheritance, Promise chains)
- Update delta table when porting or discovering new features
- Always build + test after porting
