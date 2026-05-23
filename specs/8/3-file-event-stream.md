---
status: draft
---

# File-event streaming — workspace inotify channel

A real-time event stream of file operations happening inside the agent
container, surfaced back to `gated` and downstream consumers (PR bot,
dashboard UI, audit log). Today the only signal that an agent touched
30 files is the parsed text of its Bash/Write tool output — we lose
the structured truth at the boundary between agent and host.

Inspired by OpenHands' `EventStreamRuntime` pattern: a thin watcher
in the container emits `FileWriteEvent` / `FileReadEvent` /
`FileDeleteEvent` over the same channel the agent already uses. Same
shape; arizuko's primitives differ (MCP unix socket, SQLite,
per-group container) but the contract is identical.

## Why this is useful

Three concrete pulls, in order of weight:

1. **PR bot needs "files touched this turn".** When an agent works
   on a `git:` repo, the post-turn `commit` step today has to either
   `git status --porcelain` (works, but no in-turn granularity) or
   parse Write tool output text (fragile). A canonical event stream
   gives the bot a precise per-turn changeset including transient
   files (touched then deleted) that don't show up in git diff.
2. **Dashboard progress feed.** The web UI shows "Agent is thinking…"
   for the full turn. With file events, dashd can render a live
   feed: `wrote src/foo.go (412 lines)`, `read tests/foo_test.go`,
   `deleted .bak`. Operators see what the agent is actually doing.
3. **Safety audit trail.** Today, when an agent goes off-rails and
   nukes a file, the only forensic trace is the tool-call log inside
   the chat transcript. A persisted file-event log gives the security
   review a clean answer to "what did this turn write to disk?" —
   independent of whatever the agent claimed in its narration.

Out of scope as a motivation: file events are NOT a fence — the agent
already wrote the file by the time the event fires. Use HITL firewall
(`specs/8/4`) for prevention; file events are for observability.

## Architecture

The watcher lives **inside the container**, not on the host. Host-side
inotify against the bind-mounted workspace works for overlayfs but
breaks on tmpfs and on certain Docker storage drivers (notably ZFS
and btrfs with `overlay2` quirks). Putting it in the container makes
the contract uniform across mount backends and avoids the host
inheriting kernel watch quotas from every running group.

```
container (per group)
├── agent (Claude Code, TS)         existing
├── socat MCP bridge → unix sock    existing
└── filewd (Go, tiny)               NEW: inotify → JSONL → MCP frame
                                          /tmp/file-events.sock

host (gated)
├── MCP server                      existing
│   └── handles filewd frames as a
│       second-class submit-stream
└── file_events table (SQLite)      NEW
```

`filewd` is a ~300 LOC Go binary baked into the agent image
(`ant/Dockerfile`). It opens the existing MCP unix socket (the one
the agent uses), authenticates with the same `IPC_TOKEN`, and sends
file events as a new JSON-RPC method `file_event`. No new socket,
no new auth path.

Why piggyback on the existing MCP socket instead of a sidecar
channel: gated already speaks JSON-RPC over that socket, already
validates the token, already journals frames. Adding a method is
cheaper than adding a transport. The agent and `filewd` are two
clients on the same socket — fine, MCP allows multiple sessions per
endpoint.

## Event flow

```
inotify(IN_CLOSE_WRITE, IN_DELETE, IN_MOVED_TO/FROM)
  → filewd debouncer (250ms coalesce on same path)
  → JSON-RPC file_event over MCP socket
  → gated handler
      ├── INSERT INTO file_events
      ├── SSE broadcast on web:<folder> (round_done piggyback)
      └── fan-out to subscribers (gitd PR-bot, dashd live feed)
```

The watcher reads `IN_CLOSE_WRITE` rather than `IN_MODIFY` —
`IN_MODIFY` fires once per `write(2)` syscall and would emit
hundreds of events for a single `cp` of a large file. `IN_CLOSE_WRITE`
fires once per file-handle close, which is the boundary an operator
cares about.

## Event schema

Stored in a new `file_events` table; one row per event.

| Column     | Type       | Notes                                              |
| ---------- | ---------- | -------------------------------------------------- |
| id         | INTEGER PK | Autoincrement                                      |
| folder     | TEXT       | Group folder (e.g. `solo/inbox`)                   |
| session_id | TEXT       | Agent session id (turn-scoped, from `submit_turn`) |
| op         | TEXT       | `write` / `read` / `delete` / `rename`             |
| path       | TEXT       | Workspace-relative path (no host prefix)           |
| size       | INTEGER    | Bytes; 0 for delete/read                           |
| old_path   | TEXT       | Set only for `rename`                              |
| ts         | INTEGER    | Unix ns                                            |
| tool_hint  | TEXT       | Best-effort: `Write` / `Edit` / `Bash` / `unknown` |

Migration: `store/migrations/0065-file-events.sql`. Indexed on
`(folder, ts DESC)` for the dashboard query and `(session_id)` for
the per-turn changeset query.

`tool_hint` is filled by correlating the event ts with the agent's
most recent tool-call frame in the same socket — gated has both. It's
a hint, not authoritative; if the agent shelled out via Bash, the
event will be `tool_hint=Bash` even if the tool was `cp`.

## Verbs (filewd → gated)

| Method        | Payload                           | Notes                          |
| ------------- | --------------------------------- | ------------------------------ |
| `file_event`  | `{op, path, size, old_path?, ts}` | Single event                   |
| `file_batch`  | `{events: [...]}`                 | Coalesced burst (>10 events)   |
| `watch_start` | `{folder, paths: [...]}`          | Watcher lifecycle ping         |
| `watch_stop`  | `{folder, reason}`                | Watcher exit (graceful or OOM) |

Single events are sent inline. If filewd sees >10 events within its
250ms debounce window, it flushes as `file_batch` instead — cuts MCP
frame overhead for `npm install` and `make build` storms.

## Subscriber surfaces

Same fan-out pattern as `round_done`:

- **SSE** — `webd` exposes `/events/files?folder=<f>` for the
  dashboard live feed. Filters by grant (only operator + group
  members see their own folder's events).
- **gitd PR-bot** — subscribes via gated's internal pub-sub, snapshots
  `session_id`'s event list when `submit_turn` fires, hands the
  changeset to its commit/PR logic.
- **Audit query** — `SELECT * FROM file_events WHERE session_id = ?`
  via the existing `arizuko inspect` CLI surface.

## Performance and scope guardrails

- **Per-group event-rate cap**: 200 events/sec sustained, 1000/sec
  burst. Above this filewd drops events and increments a counter
  reported via `watch_stop` reason. Default-ignore patterns make
  this hard to hit in practice.
- **Default ignore patterns** (hardcoded; not operator-tunable in
  v1 to avoid the "watcher silently misses things" footgun):

  ```
  .git/**
  node_modules/**
  target/**         # Rust
  dist/**           # JS build
  .venv/**          # Python venv
  __pycache__/**
  *.log
  *.tmp
  *.swp             # vim
  .arizuko-state/   # internal scratch
  ```

- **Workspace-only**: filewd watches `/workspace` exclusively. It
  does NOT watch `/tmp`, `/home/ant/.claude/`, or any path outside
  the bind-mounted workspace. Agent self-state mutations are not
  user-visible events.
- **Recursive watch budget**: max 10000 inotify watches per group
  container. Above that, filewd refuses to watch new subdirectories
  and logs a `watch_budget_exceeded` warning. Hitting this means
  the agent generated a deep tree it shouldn't have (likely a
  runaway `find` or unpacked tarball).
- **Backpressure**: if gated's MCP socket buffer fills, filewd
  blocks on the write. We prefer dropping events to crashing the
  agent's MCP path — `file_batch` includes a `dropped_since_last`
  counter when this happens.

## Persistence policy

- **Retention**: 30 days, then drop. File-event volume is bursty;
  a single `npm install` can produce 5000 rows. Retention is per-row
  ts, not per-session.
- **No FTS**: file events are queried by path prefix or session id,
  not full-text. Standard B-tree indexes suffice.
- **Vacuum**: `gated`'s existing nightly vacuum sweep covers this
  table — no new cron.

## Honest gaps

- **Symlinks**: inotify follows the watched directory tree but not
  symlinks. If the agent symlinks `/workspace/foo → /etc/passwd`
  and reads through it, we see the symlink creation but not the
  read of `/etc/passwd`. Security review must not treat absence of
  events as proof of no access. (Containers run as non-root with
  no host-mount escape, so the practical exposure is small, but
  the audit-log claim is bounded.)
- **Large-file streaming**: a 2 GB file written in chunks fires one
  `IN_CLOSE_WRITE` at the end. We don't see progressive size during
  the write. Acceptable — the dashboard's "agent is working" feel
  doesn't need byte-by-byte updates, and the post-write `size`
  column is correct.
- **Container-vs-volume mount semantics**: when the workspace is a
  bind mount (default), inotify inside the container sees the same
  events as the host would. When it's a named volume on overlay2,
  events also work. When it's a tmpfs (only `crackbox` experimental
  egress-isolated mode), inotify works but events evaporate on
  container restart — fine, they were already streamed to gated.
- **Read events are noisy**: `IN_ACCESS` fires for every `cat`,
  every `grep`, every test runner reading a fixture. v1 emits
  `read` events but the dashboard view filters them out by default
  (only `write/delete/rename` shown). PR bot ignores `read`.
- **Atomic-rename editors**: tools that write `foo.tmp` then rename
  to `foo` (vim, gofmt with `-w`) generate a `write` of `foo.tmp`
  - `rename foo.tmp → foo`. The event stream correctly reports
    both; consumers must coalesce.
- **Filesystem types**: tested on ext4 and overlay2. ZFS and btrfs
  not in scope for v1 — inotify behaviour on COW filesystems has
  edge cases (snapshots fire spurious events on some kernels).

## Out of scope (initial ship)

- **Operator-configurable ignore patterns** — the default list is
  the contract; expand the list in code if a real workflow gets
  miscategorized.
- **Cross-container file events** — each group's events stay scoped
  to its folder. No global "all writes across the instance" view.
- **In-flight rejection** — file events are observability, not a
  guard. Preventing a write is HITL firewall's job
  (`specs/8/4-hitl-firewall.md`).
- **Diff content** — events carry size, not content. If a consumer
  needs the diff, it reads the file via davd. Storing diffs would
  multiply our storage cost by 100x for marginal value.
- **macOS/FSEvents portability** — agent containers are Linux only.
  Dev-loop containers on Docker Desktop (macOS) work because the
  Linux VM does the inotify.

## Compose & env

```
FILEWD_ENABLED=1               # default on; set to 0 to disable per-group
FILEWD_RATE_LIMIT=200          # events/sec sustained
FILEWD_WATCH_BUDGET=10000      # max inotify watches per group
FILEWD_DEBOUNCE_MS=250         # coalesce window
```

No new ports — filewd uses the existing IPC socket.

## Effort estimate

~300-500 LOC Go for `filewd` + ~200 LOC in gated for the new
JSON-RPC method, table, SSE fan-out. Migration is trivial. One
new docker image layer (the filewd binary in the ant image).
Tests are the long pole: filesystem-event tests are flaky on CI
unless you sync explicitly — use `fsnotify`'s test helpers, and
gate the smoke test behind `make test-e2e` not `make test`.

Comparable to `timed`'s initial ship (~2-3 days of focused work
for the core, another 1-2 days for the dashboard SSE integration).

## Risks

- **Inotify watch exhaustion**: kernel default `fs.inotify.max_user_watches`
  is 8192 on many distros, 65536 on modern ones. Per-group budget
  of 10000 means 6 groups saturates an old default. Ops note in
  `SECURITY.md`: bump to 1048576 on production hosts.
- **Event reordering**: inotify guarantees ordering per-watch
  descriptor, not across descriptors. When `mv a/x b/x` fires, the
  `IN_MOVED_FROM` on `a/` and `IN_MOVED_TO` on `b/` may arrive in
  either order at filewd. We pair them by cookie before emitting
  `rename`.
- **Container restart event-loss**: filewd has no persistent queue.
  If the container OOMs mid-burst, events between the last flush
  and the restart are lost. Acceptable for v1; the agent transcript
  remains the source of truth for "what was claimed". Persistent
  queue is a v2 if real audit pressure shows up.
- **Privacy creep**: `read` events on user files are surveillance.
  Default-off in the dashboard view; documented in `SECURITY.md`
  before this ships.

## Decisions

- Watcher lives **inside the container** — uniform contract across
  mount backends, no host watch-quota inheritance.
- **Reuse the existing MCP socket** — one transport, one auth path,
  one journal. New JSON-RPC method, not a new port.
- **`IN_CLOSE_WRITE`, not `IN_MODIFY`** — one event per file close,
  not per syscall.
- **Workspace-only scope** — agent self-state under
  `~/.claude/` is not observable.
- **Hardcoded ignore patterns** — no operator config in v1; the
  pattern list is part of the contract.
- **Observability, not enforcement** — prevention is HITL firewall's
  job; this spec is a feed, not a gate.
