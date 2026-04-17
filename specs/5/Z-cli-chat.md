---
status: draft
---

# CLI Chat Mode Spec

`arizuko chat` — run an agent group interactively from the terminal,
bypassing the full gateway/adapter stack.

## Command interface

```
arizuko chat [group] [flags]
  group          group folder name (default: "root")
  --new          force new session
  --instance     instance name (default: "local")
  --data         data dir override
  --no-ipc       skip MCP server
```

Also available as standalone `ant` binary (separate `cmd/ant/main.go`).

## What gets stripped vs kept

**Stripped**: SQLite store, gateway polling loop, channel adapters,
GroupQueue, router.FormatMessages, output callback routing, session
cursor tracking, impulseGate, prefix dispatch, diary annotation.

**Kept**: `container.Run()`, `BuildMounts()`, `seedSettings()`,
`seedSkills()`, `ipc.ServeMCP()` (with stubs), auth/grants, groupfolder
resolver.

## Design

**Output**: `OnOutput` callback writes to stdout via
`router.FormatOutbound` (strips internal tags). No DB writes, no
channel send.

**Follow-up messages**: host reads stdin, writes IPC files to
`/workspace/ipc/input/*.json`. Container polls every 500ms.
On EOF, write `_close` sentinel.

**Session**: `groups/<folder>/cli-session.json` stores last session ID.
Read at startup for resume, written after first `newSessionId` arrives.
`--new` flag ignores stored session.

**IPC/MCP**: start MCP server with stub `GatedFns` for channel tools
(Option A). Channel tools fail gracefully. Grant rules use `["*"]`
for trusted-user model.

**Container**: reuses `container.Run()` unchanged. `ChatJID` set to
`"cli:<folder>"`. No `-t` flag needed — agent-runner reads JSON stdin,
not interactive PTY.

## Open questions

1. **First prompt UX**: print `> ` prompt, wait for first line, launch
   container with that as initial prompt. Or launch immediately with
   placeholder?

2. **Progress during tool use**: agent-runner emits output only at
   query end. Heartbeat JSON arrives every 30s — CLI could show spinner.

3. **Multiline input**: blank-line terminates, double-blank sends? Or
   Ctrl+D?

4. **MCP tools that write to channel**: stub returns error. Could
   hook to print to stdout instead for nicer UX (not MVP).

5. **No-DB dependency**: `ant` requires either `$ARIZUKO_DATA_DIR` or
   `--data <dir>` pointing to pre-created instance dir. No auto-create.
