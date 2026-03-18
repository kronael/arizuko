# Extensions System

**Status**: planning (updated 2026-03-10)

Extension points and plugin architecture for arizuko.
Goal: make the system extensible without modifying core code.

## Extension Points Summary

| Point         | Location            | Extensible By | Mechanism        |
| ------------- | ------------------- | ------------- | ---------------- |
| Channels      | external containers | Developer     | HTTP protocol    |
| Actions       | MCP tools           | Agent/Plugin  | Registry + MCP   |
| Routing Rules | router/             | Agent         | MCP tools        |
| Sidecars      | container/          | Agent         | Container config |
| Mounts        | container/          | Agent         | Container config |
| Skills        | container/skills/   | Agent         | File-based       |
| Tasks         | timed/              | Agent         | IPC actions      |
| Diary         | diary/              | Agent         | File-based       |

## 1. Action Registry

Shipped as `ipc/` package. All 16 MCP tools registered in
a single handler with gateway callbacks injected at creation
time. Agent discovers tools via MCP `tools/list`. Authorization
via `auth.Authorize` at runtime.

See `specs/7/10-ipc.md` for tool list and architecture,
`specs/7/11-auth.md` for tier assignments.

## 2. Channel Interface

Channels are now external HTTP processes, not Go interfaces.
See `specs/7/1-channel-protocol.md` for the full protocol
spec including registration, capabilities, auth, and
transport options.

## 3. Sidecar System

**Current**: Per-group sidecar config in `GroupConfig.Sidecars`.
Launched as separate containers with Unix socket IPC.

### Decided

1. **Sidecar protocol**: MCP over unix socket uniformly. All
   sidecars expose MCP tools on a socket in `/workspace/ipc/`.
   Same transport as `ipc` — agent connects via MCP client.
   No HTTP, no gRPC. One protocol means one client library.

2. **Sidecar lifecycle**: persistent daemons, not per-agent.
   Like `ipc` and `timed` — started by compose, run
   continuously, survive agent container restarts. Shared
   sidecars between groups are possible via socket path.

3. **Sidecar discovery**: via MCP `tools/list`. Agent connects
   to each socket in `/workspace/ipc/`, calls `tools/list`,
   merges available tools. No manifest, no gateway query.

4. **Sidecar auth**: env var passthrough (current). Sidecars
   inherit group-scoped env from compose config. No scoped
   tokens in v1 — sidecar runs at the same trust level as
   the agent it serves.

## 4. Skills System

**Current**: `container/skills/` seeded into agent session.
Each skill has `SKILL.md` with prompt injection.

### Decided

1. **Skill loading**: on spawn, if destination does not exist.
   Gateway copies `container/skills/` to session dir on first
   spawn per group. Agent owns its copy — changes persist.
   Canonical definitions at `/workspace/self/container/skills/`
   (read-only mount) for `/migrate` diffing.

2. **Skill dependencies**: deferred. No dependency resolution
   in v1. Skills are standalone units. If a skill needs another,
   document it in SKILL.md — human ensures both are present.

3. **Skill scope**: three levels, no inheritance:
   - `container/skills/` — global, baked into image (read-only)
   - `groups/<folder>/.claude/skills/` — per-group, persistent
   - `.claude/skills/` — per-session, seeded from global on
     first spawn, then agent-owned

4. **Skill updates**: `MIGRATION_VERSION` integer + `/migrate`
   skill. Root agent runs `/migrate`, which diffs canonical vs
   session skills for every group, copies changed skill dirs,
   runs numbered migration `.md` scripts. No hot reload.

5. **Skill marketplace**: deferred indefinitely. No external
   skill install in v1.

### Skill format

```
<name>/
  SKILL.md              # Required: prompt injection
  CLAUDE.md             # Optional: additional context
  migrations/           # Optional: numbered upgrade scripts
```

## 5. Routing Rules

Flat routes table (shipped). Keyed by `jid` + `seq`.
7 rule types: command, verb, pattern, keyword, sender,
trigger, default. First match wins.

Agents modify routing via MCP `set_routing_rules` tool
(tier 0-2). Dynamic — no restart needed.

## 6. Permission Tiers

**Decided**: folder-depth model. 4 tiers, no inheritance,
no escalation in v1, no custom tiers.

| Tier | Name   | Depth | Example             |
| ---- | ------ | ----- | ------------------- |
| 0    | root   | 0     | `root`              |
| 1    | world  | 1     | `atlas`             |
| 2    | agent  | 2     | `atlas/support`     |
| 3    | worker | 3+    | `atlas/support/web` |

Tier = `min(folder.split("/").length, 3)`, except `root`
which is always 0. Folders deeper than 3 are clamped to
worker. Registration rejects depth > 3.

No inheritance — tier computed from depth, not parent.
No escalation — agents cannot temporarily gain higher
tier. `escalate_group` sends a message to the parent,
it does not grant permissions.

No custom tiers or named roles. The 4-tier model covers
all current needs. Capabilities are implicit in tier
number via `maxTier` on each action in the registry.

See `specs/7/11-auth.md` for per-action tier assignments
and mount enforcement table.

## 7. Plugin Architecture

Plugins are docker containers with `.toml` configs in the
data dir `services/` directory. See `specs/7/0-architecture.md`
for the compose generation format.

**Deferred**. Discovery, install workflow, versioning,
security, and dependency resolution are all deferred until
built-in channels and sidecars are validated. The current
model (\*.toml + compose generation) works.

## Non-goals (for now)

- External plugin marketplace (deferred indefinitely)
- Hot reload (deferred)
- WebAssembly support (deferred)
