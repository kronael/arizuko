---
status: shipped
---

# Generic Container Commands

## Overview

Decouple the container sandbox from the command that runs inside it.
The container provides the environment (mounts, user, timezone, tools).
The command is caller-supplied. The Claude agent runner is the default
command, not the only one.

## Changes

### Dockerfile

Remove `ENTRYPOINT`. Gateway supplies command via docker args.
The entrypoint script (`/app/entrypoint.sh`) remains in the image
but is invoked explicitly.

### container-runner.ts

Rename `runContainerAgent` → `runContainerCommand`.

New signature (conceptual):

```typescript
function runContainerCommand(
  group: GroupConfig,
  input: ContainerInput | string, // JSON for agent, plain text or nothing for raw
  command?: string[], // undefined = agent default ['/app/entrypoint.sh']
  onProcess,
  onOutput,
): Promise<ContainerOutput>;
```

`buildContainerArgs()` appends command after image name:

```typescript
args.push(CONTAINER_IMAGE, ...command);
// agent:  kanipi-agent /app/entrypoint.sh
// bash:   kanipi-agent bash -c "git pull"
```

Two internal paths based on whether `command` is supplied:

**Agent path** (command undefined):

- Seed skills, CLAUDE.md, output-styles
- Init settings with env injection
- Write gateway-caps
- Write ContainerInput JSON to stdin
- Parse OUTPUT_START/END markers from stdout
- Track sessions in DB

**Raw path** (command supplied):

- Skip all agent ceremony
- Optionally pipe plain text to stdin (message content for scripts that read it)
- Capture stdout as result text
- No marker parsing — accumulated stdout IS the result
- No session tracking
- Same timeout, logging, cleanup

### Task scheduler

New column `command` on `scheduled_tasks` table (nullable TEXT).
When set, `runContainerCommand()` is called with that command
in raw mode. When null, agent mode (current behavior).

The `schedule_task` IPC action gains an optional `command` field:

```typescript
{
  targetJid: string,
  prompt: string,               // for agent mode
  command?: string,             // e.g. "bash -c 'cd refs && git pull'"
  schedule_type: 'cron' | 'interval' | 'once',
  schedule_value: string,
  context_mode?: 'group' | 'isolated',
}
```

`prompt` and `command` are mutually exclusive. `command` implies
raw mode + isolated context.

### Routing table

The `target` column currently holds a folder name. Extend to
support command targets:

```
target = "atlas"                        → agent on atlas folder
target = "atlas|bash -c 'echo hi'"      → bash in atlas sandbox
```

Pipe delimiter separates folder from command. No pipe = agent default.
The folder determines the sandbox (mounts, permissions, tier). The
command determines what runs.

## Migration

- `ALTER TABLE scheduled_tasks ADD COLUMN command TEXT`
- Existing tasks unaffected (command = NULL = agent mode)
