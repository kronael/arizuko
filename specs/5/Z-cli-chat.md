---
status: unshipped
---

# CLI chat mode

`arizuko chat [group] [--new] [--instance] [--data] [--no-ipc]` — run
an agent group interactively from the terminal, bypassing
gateway/adapters.

Strip: SQLite store, gateway poll loop, channel adapters, GroupQueue,
router, cursor tracking, impulse gate, prefix dispatch, diary.
Keep: `container.Run()`, `BuildMounts()`, `seedSettings`/`seedSkills`,
`ipc.ServeMCP` with stubs, auth/grants, groupfolder resolver.

Output: `OnOutput` callback → stdout via `router.FormatOutbound`.
Follow-ups: stdin → IPC files, `_close` on EOF.
Session: `groups/<folder>/cli-session.json` (resume; `--new` ignores).
MCP tools writing to channel: stubs fail gracefully.

`ChatJID = "cli:<folder>"`. Requires existing instance dir (no
auto-create).

Rationale: local debugging and demo path without full stack.

Unblockers: first-prompt UX, progress during tool use (heartbeat/
spinner), multiline input (blank line terminates?).
