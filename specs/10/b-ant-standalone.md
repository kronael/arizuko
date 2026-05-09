---
status: unshipped
---

# Ant — agent-as-a-folder

Ant runs a Claude agent from a folder. The folder defines persona,
skills, memory, and tools. You can chat with it, schedule it, or
expose it as an MCP server. arizuko is the multi-agent orchestrator
built on top of ant.

## The folder is the agent

```
my-agent/
  SOUL.md          — persona, voice, identity
  CLAUDE.md        — operator runbook the agent reads on each turn
  skills/          — SKILL.md files (custom + curated)
  diary/           — running log, persistent across runs
  secrets/         — folder-scoped credentials (env-injected)
  MCP.json         — optional, declares MCP servers the agent gets
  workspace/       — the agent's working directory (cwd)
```

That's the unit. Everything else is delivery mechanism.

## One command, IO mode by flag

```
ant <folder>                              # interactive chat (stdin/stdout)
ant <folder> --prompt="<text>"            # one-shot, exit on completion
ant <folder> --mcp [--socket=<path>]      # expose as MCP server
ant <folder> --sandbox=none|dockbox|crackbox   # optional isolation
```

The runtime is the same in every mode: spawn `claude` with the
folder's skills + secrets + workspace mounted, operator runbook
injected, diary writes routed to `<folder>/diary/`, MCP servers
wired per `MCP.json`. The flags only pick where input comes from
and where output goes:

- default — terminal stdin/stdout, interactive
- `--prompt` — single message in, response out, exit
- `--mcp` — listen on a socket, accept JSON-RPC `send_message` /
  `get_round` (same shape as slink-MCP), other systems drive it

`--sandbox` is orthogonal to all three IO modes — pick any
combination. Default sandbox: `none` for terminal use, `dockbox`
when arizuko's `gated` invokes ant.

## Skills curation

The arizuko monorepo's `ant/skills/` has 38 skills, most arizuko-
shaped (gateway-coupled). Public ant ships only the portable subset:

- **Keep**: `diary`, `facts`, `recall-memories`, `compact-memories`,
  `users`, `commit`, `dispatch`, `find`, `bash`, `cli`, plus a memory-
  routing `CLAUDE.md` snippet
- **Drop**: anything that imports gated IPC tools (`recall-messages`,
  `schedule_task`, slink-\* skills, channel-aware variants)
- **Rule**: if `grep -l '@gated\|@arizuko\|gated\.sock' SKILL.md`
  matches, the skill is arizuko-only and goes into a separate
  `ant-arizuko/skills/` bundle that arizuko's compose layers on top
  of ant.

## Container runtime — Go binary, replaces TS

Today's TS `ant/` runtime gets replaced by a Go binary that:

- Drives `claude -p --output-format stream-json --input-format
stream-json --permission-mode bypassPermissions
--mcp-config /tmp/mcp.json [--resume <sid>]`
- Implements the same container contract: stdin `ContainerInput`,
  stdout ARIZUKO markers
- Ports the existing TS hooks: `PostToolUse` (mid-loop IPC drain),
  `PreCompact` (transcript copy), Bash secret-scrub

Same Go binary is the host-side `ant` CLI and the in-container
runtime. arizuko's `gated` keeps spawning `ant:latest` unmodified —
the contract doesn't change.

Unblockers (carried over from the prior R-ant-go-cli spec):

- Pin a specific `claude` CLI version — stream-json schema is
  undocumented
- Verify `--resume <sid>` against workspace mount layout
- Map CLI exit codes to `ContainerOutput.error`
- Cutover via `AGENT_IMAGE` env var: soak on one group, then promote

## Components

```
ant/
  cmd/ant/main.go           — CLI: ant &lt;folder&gt; [--prompt|--mcp] [--sandbox]
  pkg/agent/                — folder loader, skill resolver, memory wiring
  pkg/host/                 — sandbox abstraction (dockbox + crackbox)
  pkg/runtime/              — claude-CLI driver, hooks, IPC
  Dockerfile                — `ant:latest` agent image
  skills/                   — curated portable skills
  README.md                 — what ant is, how to run it
  CLAUDE.md                 — operator runbook
```

## Docs

`ant/README.md` answers three questions in <300 words:

1. **What is ant?** Run a Claude agent from a folder. The folder
   defines persona, skills, memory.
2. **How do I run it?** `ant <folder>` — needs `ANTHROPIC_API_KEY`
   in env or the folder's `secrets/`. Add `--prompt="..."` for
   one-shot, `--mcp` to expose as a server.
3. **Why a folder?** Agents are forkable, version-controllable,
   shareable. Copy the folder, tweak SOUL.md, ship a new agent.

No marketing. No feature matrix. Three questions.

## How arizuko uses ant

- Each `groups/<folder>/` IS an ant folder. Same shape, same skills,
  same memory layout.
- `arizuko chat <instance> [group]` routes to `ant
<data-dir>/groups/<group>` under the hood. Defaults to the root
  group when `[group]` is omitted (today's behavior).
- `gated` spawns agents via `ant <group-folder> --prompt="<input>"
--sandbox=dockbox` (or via the `ant:latest` image directly,
  which is the same thing).
- arizuko adds: channels, queue, scheduler, cron, A2A routing, web UI.
  The agents themselves are ant.

## Out of scope

- Plugin system / dynamic skill loading — out of scope; ant only
  loads what's in the folder at spawn time
- Cross-agent messaging / slink — that's arizuko-shaped, not ant
- Workflow engine / scheduler — `timed` stays in arizuko

## Acceptance

- `mkdir my-agent && cp -r examples/starter/* my-agent/ &&
ant my-agent` works without arizuko anywhere on the system
- The agent persists diary writes across restarts (folder is the
  source of truth)
- arizuko's compose continues to work unchanged: it consumes
  `ant:latest` image the same way today; `arizuko chat <inst> <group>`
  routes via `ant`
- `ant/` has zero arizuko-internal Go imports — same orthogonality
  test as `crackbox/`

## Shipped this pass (v0.33.5)

Foundation only — nothing the runtime relies on changes. The TS path
in `ant/src/` and `arizuko-ant:latest` are untouched.

- `ant/cmd/ant/main.go` — flag-parsing stub. Body returns
  `EX_USAGE (64)`; `--help` exits 0; the three documented flag
  groups parse correctly.
- `ant/pkg/agent/loader.go` + tests — `LoadFolder(path) (*Folder, error)`,
  `ErrNotFound`, three test cases (ok / missing / not-a-dir).
- `ant/pkg/host/doc.go`, `ant/pkg/runtime/doc.go` — package stubs
  with one-line intent.
- `ant/scripts/curate-skills.sh` — portability gate. Reports 37
  portable + 1 arizuko-only on the current tree. No skills moved.
- `ant/README.md` — three-question intro; documents the partition
  count and the deferred work.
- Migration `095-v0.33.5-ant-foundation.md` + version bump.

Not shipped this pass: the runtime port (replacing `src/index.ts`),
the sandbox backends (`pkg/host` is empty), the MCP server mode
(`--mcp` parses but exits 64), the move of arizuko-only skills into
`ant-arizuko/skills/`. Status flips to `shipped` when those land.

## Open questions

1. **`ant/CLAUDE.md` collision** — the file already exists as the seed
   for every group's in-container `~/.claude/CLAUDE.md`. The standalone
   package wants its own `CLAUDE.md` as an operator runbook. Resolution
   options: rename the seed (`ant/templates/agent-CLAUDE.md`), or keep
   the seed and document the package via README only. Decide before
   the runtime port lands.
2. **Binary-name collision with `ant/ant` shell launcher** — building
   `ant/cmd/ant` from the repo root produces `./ant`, which is also
   the existing bash launcher path (`ant/ant`). Mitigation this pass:
   build via `go build -o ./tmp/ant-bin ./ant/cmd/ant`. At cutover:
   replace the bash launcher with the Go binary, or rename the Go
   binary (`antd`? `ant-cli`?).
3. **Root Makefile wiring** — `ant` is not in `COMPONENTS` because the
   existing `ant/Makefile` does not implement `build`/`lint`/`test`
   targets compatible with the recursive root targets. Adding it
   would either need additive Go targets in `ant/Makefile` or a
   separate target for the Go binary alone.
4. **Pin the `claude` CLI version** before the runtime port — the
   stream-json schema is undocumented (carryover from R-ant-go-cli.md).
5. **Skill move semantics** — when moving arizuko-only skills into
   `ant-arizuko/skills/`, decide whether the in-container seed comes
   from one tree (`ant/skills/` + `ant-arizuko/skills/`) or whether
   `ant-arizuko/` carries its own copy.

## Relation to other specs

- [../8/12-crackbox-sandboxing.md](../8/12-crackbox-sandboxing.md) —
  ant's `--sandbox=crackbox` backend depends on `crackbox/pkg/host`
  being stable
