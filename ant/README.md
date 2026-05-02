# ant

A standalone Claude agent runner. Wraps the `claude` CLI, mounts a
folder of identity + skills + memory + workspace, optionally puts the
agent behind a sandbox, and exposes the resulting agent over MCP.

## What is it?

A binary you can point at any directory: `ant <folder>`. The folder
holds the agent's `SOUL.md` / `CLAUDE.md` (identity), `skills/`
(callable capabilities), `diary/` (memory), `secrets/` (env injection),
`MCP.json` (tool wiring), and `workspace/` (scratch). One folder, one
agent. Run it interactively, give it a one-shot `--prompt`, or expose
it as an MCP server (`--mcp`) for another agent to call.

The same skills directory and prompt machinery used inside arizuko —
just without the router, channel adapters, or DB. Sandbox is optional
(`--sandbox=none|dockbox|crackbox`); default is unsandboxed local exec.

## Why?

arizuko's per-group Claude container is currently a TS runtime
(`ant/src/index.ts`) wrapping the Anthropic Agent SDK. That works, but
it's the only Node code in an otherwise Go codebase, and it's not
usable outside arizuko. This package collapses both: a Go binary that
drives the official `claude` CLI in stream-json mode, with the
arizuko-specific glue split into a separate component
(`ant-arizuko/`). The result runs anywhere — laptop, CI, a different
host — without bringing the router along.

## How do I use it?

```sh
ant ~/my-agent                          # interactive REPL
ant ~/my-agent --prompt="summarise PR"  # one-shot
ant ~/my-agent --mcp --socket=/tmp/x.sock
ant ~/my-agent --sandbox=crackbox --prompt=...
```

## Status

Foundation only — see [`specs/5/b-ant-standalone.md`](../specs/5/b-ant-standalone.md).
The package skeleton (`cmd/ant`, `pkg/agent`, `pkg/host`,
`pkg/runtime`) is in place; the runtime port (replacing
`ant/src/index.ts`), the sandbox backends, and the skills curation
move into `ant-arizuko/` are tracked there but not yet shipped. The
existing TS runtime in `src/` still drives `arizuko-ant:latest`.

## Layout

```
ant/
  cmd/ant/main.go         CLI entrypoint (flag stub)
  pkg/agent/loader.go     Folder layout resolution
  pkg/host/               Sandbox abstraction (deferred)
  pkg/runtime/            Claude CLI driver (deferred)
  scripts/curate-skills.sh  portable vs arizuko-only skill partition
  skills/                 In-tree skills (current arizuko ant)
  src/                    Existing TS runtime (still in use)
  Dockerfile, Makefile    Builds arizuko-ant:latest (TS)
```

## Skill partition

`scripts/curate-skills.sh` partitions `skills/` into portable vs
arizuko-only. A skill is arizuko-only if its `SKILL.md` mentions
`@gated`, `@arizuko`, or `gated.sock`; everything else is portable
(loadable by Claude Code without arizuko). Current count: **37
portable, 1 arizuko-only** (`self`). The arizuko-only set will move
to `ant-arizuko/skills/` when curation actually runs; for now nothing
has moved.

## Orthogonality

```sh
grep -rE 'github\.com/[^/]+/arizuko/(store|core|gateway|api|chanlib|chanreg|router|queue|ipc|grants|onbod|webd|gated)' ant/cmd/ ant/pkg/  # returns empty
```

`ant/cmd/`, `ant/pkg/agent`, `ant/pkg/host`, and `ant/pkg/runtime`
import nothing from arizuko-internal packages. Like crackbox, ant
shares arizuko's single `go.mod` but the import graph keeps it
shippable as a standalone binary.
