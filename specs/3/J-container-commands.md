---
status: shipped
---

# Generic Container Commands

Decouple container sandbox from the command inside. Container provides
environment (mounts, user, timezone, tools). Command is caller-supplied.
Claude agent runner is default, not the only one.

## Two paths

**Agent path** (no command supplied):

- Seed skills, CLAUDE.md, output-styles, settings, gateway-caps
- Write `ContainerInput` JSON to stdin
- Parse OUTPUT_START/END markers from stdout
- Track sessions in DB

**Raw path** (command supplied):

- Skip agent ceremony
- Optionally pipe plain text to stdin
- Accumulated stdout IS the result (no markers)
- No session tracking
- Same timeout, logging, cleanup

## Task scheduler

`scheduled_tasks.command` (nullable TEXT). Set = raw mode. Null = agent
mode. `prompt` and `command` are mutually exclusive.

## Routing target syntax

```
target = "atlas"                     -> agent on atlas folder
target = "atlas|bash -c 'echo hi'"   -> bash in atlas sandbox
```

Pipe separates folder from command. Folder determines sandbox; command
determines what runs.
