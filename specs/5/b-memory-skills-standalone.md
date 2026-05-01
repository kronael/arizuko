---
status: unshipped
---

# Ant standalone — Claude Code distribution

Make `ant/` a shippable sibling of `crackbox/`: own CLI, own Docker
image, own docs, runnable outside arizuko. arizuko keeps consuming it
exactly as it does crackbox today (via the binary + image contract,
not by importing internals).

## What ant becomes

A minimal Claude Code distribution that bundles:

- the `claude` CLI
- a curated skill set (memory + workflow + tooling, ~38 today —
  prune aggressively before public release)
- a sandbox-spawn entrypoint: `ant run` boots the agent inside
  dockbox (Docker today) or crackbox (KVM, when phase 6/12 lands)

Outside-arizuko use case: drop a folder, get a Claude agent running
in an isolated sandbox with secrets, skills, and memory wired up. No
gateway, no SQLite bus, no group hierarchy.

## Components

```
ant/
  cmd/ant/main.go    — CLI: `ant run`, `ant chat`, `ant exec`
  pkg/host/          — sandbox abstraction (dockbox impl shipped;
                       crackbox impl pulls in github.com/onvos/arizuko/
                       crackbox/pkg/host once that lib is stable)
  Dockerfile         — agent image (today's image, kept)
  skills/            — curated SKILL.md files
  README.md          — what ant is, how to run it
  CLAUDE.md          — operator runbook
```

CLI shape mirrors crackbox:

```
ant run [--sandbox=dockbox|crackbox] [--workspace=<dir>] [--skill=<glob>...]
ant chat <workspace>
ant exec <workspace> -- <cmd>
```

## Skills curation (the prune)

Today's 38 skills are arizuko-shaped (gateway-coupled, channel-aware).
Public ant ships only the portable subset:

- **Keep**: `diary`, `facts`, `recall-memories`, `compact-memories`,
  `users`, `commit`, `dispatch`, `find`, `bash`, `cli`, plus a memory-
  routing `CLAUDE.md` snippet
- **Drop**: anything that imports gated IPC tools (`recall-messages`,
  `schedule_task`, slink-\* skills, channel-aware variants)
- **Decision**: which skills stay = which work with ZERO arizuko
  dependency. Rule: if `grep -l '@gated\|@arizuko\|gated\.sock' SKILL.md`
  matches, it's arizuko-only and goes to a separate `ant-arizuko/skills/`
  bundle that arizuko's compose layers on top.

## Sandbox abstraction

`ant run` resolves the sandbox backend:

- `--sandbox=dockbox` (default) — Docker container, today's path
- `--sandbox=crackbox` — KVM VM via `crackbox/pkg/host` (phase 6/12)

The interface is the same: spawn → workspace mount + secret env →
run agent → wait → return. Same contract as `crackbox run --kvm`.

## Docs

`ant/README.md` answers three questions in <300 words:

1. What is ant? (Claude Code + memory + sandbox spawn, one binary)
2. How do I run it? (`ant run` in a workspace dir; needs `OPENAI_API_KEY`
   or `ANTHROPIC_API_KEY` in env)
3. How does it differ from running `claude` directly? (sandboxed,
   bundled skills, persistent memory, restricted egress)

No marketing. No feature matrix. Three questions, three answers.

## Container runtime (replaces TS ant)

The Go binary also replaces today's TS `ant/` runtime inside the
container. `ant run` (host CLI) and the in-container entry point
share the same Go codebase: spawning side calls
`ant agent --workspace=...` inside the sandbox, which then drives
`claude -p --output-format stream-json --input-format stream-json
--permission-mode bypassPermissions --mcp-config /tmp/mcp.json
[--resume <sid>]` — same shape today's TS entrypoint already uses.

Container contract unchanged: stdin `ContainerInput`, stdout ARIZUKO
markers. arizuko's `gated` keeps spawning `ant:latest` unmodified.

Hooks (port from TS):

- `PostToolUse` — drain IPC input dir, print
  `hookSpecificOutput.additionalContext`. Mid-loop injection path.
- `PreCompact` — copy transcript to a side store before compaction.
- Bash secret-scrub — guard against accidental secret echo.

Unblockers from the prior `R-ant-go-cli` spec (now folded here):

- Pin a specific `claude` CLI version — stream-json schema is
  undocumented; floating-version risk
- Verify `--resume <sid>` against the workspace mount layout
- Map CLI exit codes to `ContainerOutput.error`
- Cutover via `AGENT_IMAGE` env var: soak on one group, then
  promote to all

## Out of scope

- Plugin system / dynamic skill loading — see [E-plugins.md](E-plugins.md)
- Cross-agent messaging / slink — that's arizuko-shaped, not ant
- Workflow engine / scheduler — `timed` stays in arizuko

## Acceptance

- `cd ~/some-project && ant run` boots a Claude agent in a Docker
  container with the workspace mounted and the API key in env
- The agent has memory persistence (diary writes survive restart)
- arizuko's compose continues to work unchanged (it consumes
  `ant:latest` image the same way it does today)
- `ant/` has zero arizuko-internal Go imports — same orthogonality
  test as `crackbox/`

## Relation to other specs

- [../6/12-crackbox-sandboxing.md](../6/12-crackbox-sandboxing.md) —
  ant's `--sandbox=crackbox` backend depends on `crackbox/pkg/host`
  being stable.
- [Z-cli-chat.md](Z-cli-chat.md) — once ant ships, `arizuko chat
<group>` can become a thin wrapper around `ant chat <workspace>`.
