# Extending arizuko

Catalog of extension points. Keep current as the system evolves.

## Extension points

| Point         | Location               | Extensible by | Mechanism        |
| ------------- | ---------------------- | ------------- | ---------------- |
| Channels      | external containers    | Developer     | HTTP protocol    |
| Actions       | MCP tools              | Agent/Plugin  | Registry + MCP   |
| Autocalls     | `gateway/autocalls.go` | Gateway dev   | Registry slice   |
| Routing rules | `router/`              | Agent         | MCP tools        |
| Mounts        | `container/`           | Agent         | Container config |
| Skills        | `ant/skills/`          | Agent         | File-based       |
| Tasks         | `timed/`               | Agent         | IPC actions      |
| Diary         | `diary/`               | Agent         | File-based       |

## Adding an autocall

Autocalls inject zero-arg, one-line, pure-read facts into the
`<autocalls>` block at the top of every prompt. Cheaper than an MCP
tool when the schema cost exceeds the data returned: no agent-visible
schema, no tool call, one line of output per turn.

Rules:

- Result is ≤ 1 line of text. Empty string = skip the line.
- No args, no I/O, no locks. Must resolve in microseconds.
- Derives from `AutocallCtx` (instance, folder, chatJID, topic,
  session, tier, now).
- If any of these don't hold, use an MCP tool instead.

Add an entry to the registry slice in `gateway/autocalls.go`:

```go
{"world", func(c AutocallCtx) string {
    return strings.SplitN(c.Folder, "/", 2)[0]
}},
```

Then update `ant/skills/self/SKILL.md` autocalls section and ship a
migration under `ant/skills/self/migrations/`.

## Inspect tools

Read-only MCP introspection family, registered in `ipc/inspect.go`:
`inspect_messages`, `inspect_routing`, `inspect_tasks`,
`inspect_session`. Delegate to `store.*` accessors; no destructive
operations (those stay in `control_*`). Tier 0 sees all instances; tier
≥1 is scoped to its own folder subtree. Extend by adding a handler to
`registerInspect` and wiring a fn into `ipc.StoreFns`.

## Adapter `/health` contract

`chanlib.NewAdapterMux` requires a non-nil `isConnected func() bool`
(panics otherwise). `GET /health` returns 200 `{status:"healthy",...}`
only when `isConnected()` is true; otherwise 503
`{status:"disconnected"}`. "Connected" means the platform side is live
(baileys socket open, mastodon stream up, …) — not just that the
process started. Docker `HEALTHCHECK` flips the container to
`unhealthy` automatically. Every Go adapter and `whapd` implement it.

## Skills

Three scopes, no inheritance:

- `ant/skills/` — global, baked into image, read-only
- `groups/<folder>/.claude/skills/` — per-group, persistent
- `.claude/skills/` — per-session, seeded from global on first spawn

Canonical definitions at `/workspace/self/ant/skills/` (ro mount) for
`/migrate` diffing. `MIGRATION_VERSION` integer + `/migrate` skill
drive upgrades.

Skill layout:

```
<name>/
  SKILL.md              # required: prompt injection
  CLAUDE.md             # optional
  migrations/           # optional numbered upgrade scripts
```

## Permission tiers

Folder-depth model. Tier = `min(folder.split("/").length, 3)`, except
`root` = 0. Registration rejects depth > 3.

| Tier | Name   | Depth | Example             |
| ---- | ------ | ----- | ------------------- |
| 0    | root   | 0     | `root`              |
| 1    | world  | 1     | `atlas`             |
| 2    | agent  | 2     | `atlas/support`     |
| 3    | worker | 3+    | `atlas/support/web` |

No inheritance, no escalation, no custom tiers. `escalate_group` sends
a message to the parent; it does not grant permissions. See
`specs/4/11-auth.md` and `specs/4/19-action-grants.md`.
