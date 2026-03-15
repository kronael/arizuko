## <!-- trimmed 2026-03-15: TS signatures removed, rich facts only -->

## status: shipped

# Generic Container Commands

Decouple container sandbox from the command inside it. Container
provides environment (mounts, user, timezone, tools). Command is
caller-supplied. Claude agent runner is default, not the only one.

## Two Paths

**Agent path** (no command supplied):

- Seed skills, CLAUDE.md, output-styles, settings, gateway-caps
- Write ContainerInput JSON to stdin
- Parse OUTPUT_START/END markers from stdout
- Track sessions in DB

**Raw path** (command supplied):

- Skip all agent ceremony
- Optionally pipe plain text to stdin
- Accumulated stdout IS the result (no marker parsing)
- No session tracking
- Same timeout, logging, cleanup

## Task Scheduler Column

`scheduled_tasks.command` (nullable TEXT). When set, raw mode.
When null, agent mode. `prompt` and `command` are mutually exclusive.

## Routing Table Syntax

```
target = "atlas"                     -> agent on atlas folder
target = "atlas|bash -c 'echo hi'"   -> bash in atlas sandbox
```

Pipe delimiter separates folder from command. No pipe = agent default.
Folder determines sandbox (mounts, permissions, tier). Command
determines what runs.
