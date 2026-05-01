---
status: shipped
---

> Shipped 2026-04-22: `inspect_messages`, `inspect_routing`,
> `inspect_tasks`, `inspect_session`. `inspect_logs` and
> `inspect_health` deferred — require journal / docker-socket access
> the agent container doesn't have.
>
> Planned (2026-05-01): `inspect_messages` gains a `since` param
> (forward time-window read) so the agent can pull only new rows
> after a digest cron. Pairs with the autocalls `unread`/`errors`
> extensions in [31-autocalls.md](31-autocalls.md).

# Inspect Tools — operational introspection MCP surface

The agent needs to reason about its own runtime, not just the
conversation. Today that means reading files with `Bash` + `cat`, which
is slow and opaque. A small set of `inspect_*` MCP tools gives the
agent first-class access to logs, health, and routing state.

## Family

| Tool               | Returns                                                       |
| ------------------ | ------------------------------------------------------------- |
| `inspect_messages` | local DB rows for a JID (see `3/G-history-backfill.md`)       |
| `inspect_logs`     | recent log lines for a daemon or group (`journalctl`-shaped)  |
| `inspect_health`   | service + container health (systemd + docker ps + cursor lag) |
| `inspect_routing`  | JID → folder resolution + errored flag + last container run   |
| `inspect_tasks`    | scheduled tasks, next_run, recent task_run_logs               |
| `inspect_session`  | current session ID, message count, last context reset, resume |

Read-only, tier-gated (tier 0 sees all instances; tier ≥1 sees own
group only). No destructive variants — `clear_errored`, `restart_adapter`
stay in `control_*` family (see spec 5/control-commands).

## Why not just shell

`Bash` is always available but costly:

- `journalctl -u arizuko_<inst> | grep …` requires the agent to know
  instance name, systemd unit format, grep patterns.
- Output is unbounded — no pagination, easy to blow context.
- Tier 1+ agents shouldn't have `Bash` at all.

`inspect_*` tools return shaped JSON with built-in limit/offset.
Caller gets structure, not text.

## Shape

```
inspect_logs(daemon|folder, since, limit, grep?) → {lines: [...], cursor: "..."}
inspect_health()                                  → {services: [...], containers: [...]}
inspect_routing(jid?)                             → {routes: [...], errored: [...]}
```

All follow the same pagination (`cursor` opaque string) and tier-gate
pattern as existing MCP tools.

## Files

- `ipc/inspect.go` — tool registrations, delegate to existing code:
  - `inspect_messages` → `store.MessagesBefore`
  - `inspect_logs` → exec `journalctl` bounded by `--until` + `-n`
  - `inspect_health` → `/health` endpoints + `docker ps` over the
    docker socket (root group only; tier ≥1 gets agent-scoped subset)
  - `inspect_routing` → `routes` + `messages.errored` aggregation
  - `inspect_tasks` → `scheduled_tasks` + recent `task_run_logs`
  - `inspect_session` → `sessions` row + current `messages.db` cursor

## Out of scope

- Writing logs (use existing `slog`).
- Modifying routes (see `8/32-dynamic-channels.md`).
- Arbitrary shell (use `Bash`, tier-gated).
