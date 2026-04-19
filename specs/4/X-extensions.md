---
status: partial
---

# Extensions System

Extension points in arizuko. Skills shipped; plugin marketplace
deferred.

## Extension Points

| Point         | Location            | Extensible By | Mechanism        |
| ------------- | ------------------- | ------------- | ---------------- |
| Channels      | external containers | Developer     | HTTP protocol    |
| Actions       | MCP tools           | Agent/Plugin  | Registry + MCP   |
| Routing Rules | router/             | Agent         | MCP tools        |
| Mounts        | container/          | Agent         | Container config |
| Skills        | ant/skills/         | Agent         | File-based       |
| Tasks         | timed/              | Agent         | IPC actions      |
| Diary         | diary/              | Agent         | File-based       |

## Skills (shipped)

Three scopes, no inheritance:

- `ant/skills/` — global, baked into image, read-only
- `groups/<folder>/.claude/skills/` — per-group, persistent
- `.claude/skills/` — per-session, seeded from global on first spawn

Canonical definitions at `/workspace/self/ant/skills/` (ro mount) for
`/migrate` diffing. `MIGRATION_VERSION` integer + `/migrate` skill
drive upgrades.

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
