---
status: draft
---

# File-event streaming — workspace inotify channel

A structured event stream of file operations inside the agent container,
surfaced to routd and downstream consumers. Today the only signal that an
agent touched 30 files is the parsed _text_ of its Bash/Write tool output —
we lose the structured truth at the agent/host boundary.

Inspired by OpenHands' `EventStreamRuntime`: a thin watcher in the container
emits file events over the channel the agent already uses.

## Why

1. **A PR bot needs "files touched this turn".** `git status --porcelain`
   works but has no in-turn granularity and misses transient files (written
   then deleted); parsing Write-tool text is fragile.
2. **Dashboard progress feed.** The web UI shows "Agent is thinking…" for a
   whole turn. File events let dashd render what it is actually doing.
3. **Audit trail independent of narration.** When an agent nukes a file the
   only forensic trace today is its own tool-call log — i.e. the agent's
   account of itself. A persisted event log answers "what did this turn
   write to disk?" without trusting that account.

Not a motivation: file events are **not** a fence. The write already
happened when the event fires. Prevention is
[5/19-hitl-firewall](../5/19-hitl-firewall.md); this is a feed.

## Decisions

- **Watcher runs inside the container**, not on the host. Host-side inotify
  against the bind mount works on overlayfs but breaks on tmpfs and some
  storage drivers, and would make every running group draw on the host's
  kernel watch quota. In-container makes the contract uniform across mount
  backends.
- **Reuse the existing MCP unix socket** — new JSON-RPC method `file_event`,
  not a new transport. routd already speaks JSON-RPC there, already
  authenticates the peer, already journals frames. MCP permits multiple
  sessions per endpoint, so the watcher and the agent are two clients on one
  socket: one transport, one auth path, one journal.
- **`IN_CLOSE_WRITE`, not `IN_MODIFY`.** `IN_MODIFY` fires once per
  `write(2)`, so a single large `cp` would emit hundreds of events.
  `IN_CLOSE_WRITE` fires once per handle close — the boundary an operator
  cares about.
- **Scope is the group's writable share only.** Agent self-state under
  `~/.claude/` is not observable. (Fix the exact mount against the current
  model in [5/V-web-vhosts](../5/V-web-vhosts.md) before implementing —
  the container mount layout has changed since this was drafted.)
- **Ignore patterns are hardcoded, not operator-tunable in v1** — `.git/`,
  `node_modules/`, build output, venvs, `*.log`/`*.tmp`/`*.swp`. An
  operator-editable list invites the "watcher silently missed it" footgun.
- **Observability, not enforcement.**

## Shape

`filewd`, a small Go binary in the agent image, watches the share, coalesces
bursts on a debounce window, and sends `file_event` (or a batched
`file_batch` above a burst threshold) over the MCP socket. routd persists to
a `file_events` table keyed on `(folder, ts)` and `(session_id)`, and fans
out to the same subscribers `round_done` already reaches — SSE for the
dashboard, the `gitd` PR bot, and an audit query.

Retention is time-based (bursty volume: one `npm install` can be thousands
of rows), reusing routd's existing sweep. No FTS — queries are by path
prefix or session id.

## Honest gaps

- **Symlinks.** inotify follows the watched tree, not symlinks. A read
  through `~/foo → /etc/passwd` shows the symlink creation, not the read.
  Absence of events is not proof of no access — the audit claim is bounded.
- **Read events are noise.** Every `cat`, `grep`, and fixture load fires
  one. Default the dashboard view to write/delete/rename; the PR bot ignores
  reads. `read` events on user files are surveillance — say so in
  `SECURITY.md` before shipping.
- **Atomic-rename editors** (vim, `gofmt -w`) emit write-tmp + rename.
  Correct, but consumers must coalesce.
- **Loss on container death.** No persistent queue in the watcher; events
  between the last flush and an OOM are gone. Acceptable while the
  transcript remains the record of what was _claimed_.
- **Watch exhaustion.** `fs.inotify.max_user_watches` defaults to 8192 on
  older distros. A per-group budget plus an ops note to raise it.

## Out of scope

- Operator-configurable ignore patterns (the list is the contract).
- Cross-container / instance-wide views — events stay folder-scoped.
- Diff content — events carry size, not bytes. A consumer that needs the
  diff reads the file via davd.
- Non-Linux watchers — agent containers are Linux only.
