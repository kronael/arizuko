# Agent Teams: Analysis and Decision

## What Claude Code calls "Agent Teams" vs "Subagents"

### Subagents (Agent / Task tool)

- Spawned via the `Agent` tool (formerly `Task`)
- Runs as a **separate process** with its own context window
- Shares the **same container filesystem** and mounts
- Used for: isolating large output, parallel research, enforcing tool restrictions
- Subagents **cannot** spawn further subagents
- Clean: gateway knows nothing about them, they live/die inside the container

### Agent Teams (CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS)

- Adds `TeamCreate`, `TeamDelete`, `SendMessage` tools
- Creates **separate independent Claude CLI sessions** — each with own full context
- Team members communicate via `SendMessage` (not file IPC)
- Designed for: sustained parallelism across context boundaries (long-running multi-agent pipelines)
- Spawning mechanism inside a container: runs sibling `claude` processes in the same
  container process namespace, sharing the same mounts

## Why Agent Teams Don't Fit Kanipi

1. **Parallelism already handled at gateway level** — each group gets its own container.
   Team members inside one container would share `/workspace/group`, `/web/`, IPC dirs
   and could stomp on each other with no coordination guarantees.

2. **Orphan risk** — team member processes have no tracked lifecycle in kanipi.
   When the parent container exits (idle timeout, error), sibling processes may linger.
   We already saw orphan containers (`laughing_burnell`, `modest_jackson`) from this.

3. **Stdio problem** — Gateway spawns one container → one stdio pair (stdin/stdout).
   Agent teams spawn sibling processes inside the container, each with their own stdio.
   Those sibling stdouts go nowhere: gateway never reads them. Any result, IPC message,
   or channel reply from a teammate is silently dropped.

4. **Path problem** — `~/.claude/teams/` and `~/.claude/tasks/` resolve to
   `/home/node/.claude/` inside the container, mounted from `/data/sessions/{group}/.claude/`.
   This is scoped per-group — correct for a single agent session, wrong for multi-teammate
   coordination across the team lifecycle (teams persist, containers don't).

5. **Experimental** — the feature is prefixed `EXPERIMENTAL` for a reason. Not ready
   for production chat gateway use.

6. **No benefit** — a chat agent responding to messages has no need for sustained
   multi-session parallelism. The `Agent` (subagent) tool is sufficient for any
   parallel work within a single response.

## Decision

Disabled as of this commit:

- Removed `CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS` from `settings.json` seed
  (`src/container-runner.ts`)
- Removed `TeamCreate`, `TeamDelete`, `SendMessage` from `allowedTools`
  (`container/agent-runner/src/index.ts`)

Existing `settings.json` files per group must be deleted to take effect on
running instances (they are seeded once and not overwritten).

## Subagents (Agent tool) — Keep

The `Agent` tool stays. It's stable, well-understood, and useful for:

- Parallel codebase research
- Isolating large outputs from main context
- Enforcing read-only constraints on exploration sub-tasks

`Task`, `TaskOutput`, `TaskStop` remain in allowedTools.
