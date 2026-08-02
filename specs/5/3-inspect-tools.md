---
status: shipped
---

> Shipped 2026-04-22: `inspect_messages` (`ipc/ipc.go`),
> `inspect_routing`, `inspect_tasks`, `inspect_session`,
> `inspect_identity` (`ipc/inspect.go`). `inspect_logs` and
> `inspect_health` are **dropped, not deferred** — they need journal and
> docker-socket access the agent container does not have, and giving it
> that access would defeat the container boundary.

# Inspect Tools — operational introspection MCP surface

The agent needs to reason about its own runtime, not just the
conversation. **The decision is that this is a tool family, not `Bash`.**

`Bash` is always available but wrong for this: it makes the agent know the
instance name, the systemd unit format, and grep patterns; its output is
unbounded and blows context; and a non-operator agent should not have
`Bash` at all. `inspect_*` returns shaped JSON with built-in limit/offset
and an opaque `cursor` — structure, not text.

Read-only. `registerInspect` (`ipc/inspect.go`) takes the resolved
`auth.Identity` and the caller's folder: root sees the instance, a named
folder sees itself, and any handler that accepts a `jid` binds it to the
caller's folder before answering (`db.JIDRoutedToFolder`) — the
containment rule every MCP handler owes. Mutating verbs
(`clear_errored`, `restart_adapter`) belong in their own family, never
here.

Each tool delegates to code that already exists rather than growing its
own query layer: routing → `routes` + `messages.errored`; tasks →
`scheduled_tasks` + recent `task_run_logs`; session → the `sessions` row +
the current cursor; messages → the store's history read.

## Out of scope

- Writing logs (that is `slog`).
- Modifying routes (`14/6-dynamic-channels.md`).
- Arbitrary shell (that is `Bash`, and it is gated).

<!-- UNVERIFIED as of 2026-08-02: the 2026-05-01 plan was a `since` param
on `inspect_messages` for post-digest forward reads. `since` shipped on
`find_messages` (FTS5 search, `ipc/ipc.go`) instead; `inspect_messages`
does not carry one. -->
