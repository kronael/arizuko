---
status: partial
---

# Extensions System

Extension points in arizuko. Sidecars and skills shipped; plugin
marketplace deferred.

## Extension Points

| Point         | Location            | Extensible By | Mechanism        |
| ------------- | ------------------- | ------------- | ---------------- |
| Channels      | external containers | Developer     | HTTP protocol    |
| Actions       | MCP tools           | Agent/Plugin  | Registry + MCP   |
| Routing Rules | router/             | Agent         | MCP tools        |
| Sidecars      | container/          | Agent         | Container config |
| Mounts        | container/          | Agent         | Container config |
| Skills        | ant/skills/         | Agent         | File-based       |
| Tasks         | timed/              | Agent         | IPC actions      |
| Diary         | diary/              | Agent         | File-based       |

## Sidecars (shipped)

Per-group `GroupConfig.Sidecars`. MCP over unix socket in
`/workspace/ipc/`. Persistent daemons started by compose (like `ipc`,
`timed`) — survive agent restarts. Discovery via MCP `tools/list`;
agent merges tools from each socket. Env passthrough via compose; no
scoped tokens — sidecar runs at agent's trust level.

## Skills (shipped)

Three scopes, no inheritance:

- `ant/skills/` — global, baked into image, read-only
- `groups/<folder>/.claude/skills/` — per-group, persistent
- `.claude/skills/` — per-session, seeded from global on first spawn

Canonical definitions at `/workspace/self/ant/skills/` (ro mount) for
`/migrate` diffing. `MIGRATION_VERSION` integer + `/migrate` skill
drive upgrades. Name collisions across sidecars = error at spawn.

Skill format:

```
<name>/
  SKILL.md              # required: prompt injection
  CLAUDE.md             # optional
  migrations/           # optional numbered upgrade scripts
```

## Permission tiers (shipped)

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
`11-auth.md` and `19-action-grants.md`.

## Plugin marketplace

Deferred. Current model: docker containers with `.toml` configs in
data-dir `services/` directory, included in compose generation.

## Non-goals

External marketplace, hot reload, WebAssembly.
