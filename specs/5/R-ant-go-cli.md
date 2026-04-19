---
status: unshipped
---

# ant as Go wrapper around Claude CLI

Replace TS `ant/` (Anthropic Agent SDK) with a Go binary that spawns
`claude -p --output-format stream-json --input-format stream-json
--permission-mode bypassPermissions --mcp-config /tmp/mcp.json
[--resume <sid>]`. Container contract (stdin ContainerInput, stdout
ARIZUKO markers) unchanged.

Rationale: align ant with the rest of the Go codebase, drop Node runtime,
"just another binary" per memory note. Not multi-provider (see
[G-agent-backends.md](G-agent-backends.md)).

Mid-loop injection: wire `PostToolUse` hook script that drains IPC
input dir and prints `hookSpecificOutput.additionalContext`; between
queries, check IPC on text-only turns. Port PreCompact (transcript
copy) and Bash secret-scrub hooks to scripts.

Unblockers: pin CLI version (stream-json schema undocumented), verify
`--resume` against mount layout, map CLI exit codes to
`ContainerOutput.error`. Cutover: `AGENT_IMAGE` env var, soak on one
group, then promote.
