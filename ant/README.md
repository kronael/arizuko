# ant

The agent runtime for arizuko. Wraps Claude Code (via
`@anthropic-ai/claude-agent-sdk`), injects group identity + skills +
memory, and exposes MCP tools over a gated unix socket.

## What it does

Every turn runs in a fresh KVM guest booted from this image's exported rootfs (spec 6/41).
`ant/src/index.ts` is the entrypoint: it reads stdin for the prompt,
loads the group's `PERSONA.md` / `CLAUDE.md` / `~/.claude/settings.json`,
assembles MCP servers (core `arizuko` server via socat to `gated.sock` +
third-party connectors from `settings.json`), runs the Claude Code SDK,
and delivers the result via the `submit_turn` JSON-RPC method back to
`routd`.

## TS runtime (`src/`)

The shipping runtime uses `@anthropic-ai/claude-agent-sdk` 0.3.153.
MCP servers are assembled in `src/mcp-servers.ts`:

### The harness

ant does not implement an agent loop; it wraps one. `src/claude.ts`
drives Claude Code via `@anthropic-ai/claude-agent-sdk` — spawn the
harness, reduce its message stream to frames (`ClaudeTurn`), report the
turn once over `submit_turn`. Model calls, tool execution, retries and
session state are all the harness's, never ant's.

Harness independence lives at the **process boundary**: the agent tool
surface + `submit_turn` on the unix socket routd serves is a wire
protocol, so a different harness replaces ant as its own process without
touching arizuko. Inside ant, `src/backend.ts` is the harness seam,
stated as arizuko's requirements: `Turn` in, `Frame`s out
(continuity / status / result), one `run()` per harness plus a
`HarnessProfile` declaring its capabilities. Continuity is an opaque
token each harness packs (claude a `sessionId#resumeAt` pair, codex its
thread id); a result with `retryFresh` retries the turn fresh instead of
delivering a resume-miss diagnostic. One file per harness — `claude.ts`
(default), `codex.ts` (`codex exec --json`, MCP via `-c mcp_servers.*`),
`pi.ts` (`pi -p --mode json`, native skills, no MCP client).
`ARIZUKO_BACKEND` selects; unknown values fail the turn loudly. Skills
reach codex/pi through the load-time guard in `src/skills-scan.ts` — the
same skillguard table, a different hook point. Spec `5/P` § "ant wraps a
harness, it never is one"; extension recipe in `EXTENDING.md`
§ "Swapping the agent harness".

- **`arizuko` core server** — socat bridge to `/run/ipc/gated.sock`,
  `alwaysLoad: true` so `send`/`reply`/`inspect_*`/`send_file` stay
  eager every turn.
- **Third-party connectors** — served over that SAME socket as arizuko
  tools (`ipc.StoreFns.Connectors`: built-in providers plus the
  operator's `connectors.toml`), so a connector call meets the grant
  check, the audit row and `CheckHold` like every other. A server the
  agent declares in `~/.claude/settings.json` is NOT read: that path
  handed tools straight to the harness and the agent writes that file
  itself, so it was a self-grant (BUGS J1, F76).

Nothing is deferred and there is no tool search: one server, `alwaysLoad`,
so a turn's tool list is everything the turn may call.

## Folder layout (agent view)

The guest mounts the group folder at `/home/node/`:

```
/home/node/
  PERSONA.md          operator-owned identity overlay
  CLAUDE.md           operator-owned runbook overlay
  .claude/
    CLAUDE.md         agent-managed, merged on /migrate
    settings.json     outputStyle (mcpServers is written but read by nothing)
    skills/           stock skills (seeded, 3-way merged on /migrate)
  diary/              session log
  facts/              researched knowledge
  users/              per-user memory
  workspace/          working files
  public_html/        bind-mount → /pub/<folder>/ (no auth)
  private_html/       bind-mount → /priv/<folder>/ (JWT)
```

Skills in `~/.claude/skills/<name>/` that match stock names in
`/opt/arizuko/ant/skills/<name>/` are managed (seeded + merged).
Custom-named skills are untouched.

## Skills (83 portable, 1 arizuko-only)

`scripts/curate-skills.sh` classifies every skill by SKILL.md content:
arizuko-only if it mentions `@gated`, `@arizuko`, or `gated.sock`;
portable otherwise. Current: 83 portable, 1 arizuko-only (`mcp`).

The partition exists for the future standalone-ant binary
(`specs/13/b-ant-standalone.md`) but doesn't run yet — all skills
ship in `ant/skills/`, nothing has moved to a separate `ant-arizuko/`
package.

## Standalone ant (foundation only)

`cmd/ant/`, `pkg/agent/`, `pkg/host/`, `pkg/runtime/` are the skeleton
for a Go-based standalone agent runner (spec 12/b). The package exists,
imports no arizuko-internal code, and defines the folder layout, but
the runtime port and sandbox backends aren't shipped. The TS runtime
in `src/` still drives `arizuko-ant:latest`.

## Build

```sh
make image      # docker build → arizuko-ant:latest (TS runtime)
```

The image is `FROM node:22-bookworm-slim`, installs `claude` CLI +
`socat` + runtimes (uv/bun/go/rust), copies `skills/` to
`/opt/arizuko/ant/skills/`, and sets `ENTRYPOINT ["node", "dist/index.js"]`.

`ant/Dockerfile` is the one inventory of what else the agent can reach —
chromium and `agent-browser` for the web, `ffmpeg` and `yt-dlp` for media,
`gh`, `git`, `jq`, `ripgrep`, `imagemagick`, `pandoc` and the build
toolchains. Read it there; a second list in prose drifts. Media the platform
receives is transcribed before the turn starts, by routd rather than by the
agent: `ARCHITECTURE.md` § "Inbound Media Pipeline".

## Layout

```
ant/
  src/index.ts           Entrypoint (stdin → SDK → submit_turn)
  src/mcp-servers.ts     MCP server assembly (eager vs deferred)
  src/claude.ts          Claude Code harness driver + event normalization
  CLAUDE.md              Agent identity + runbook (seeded to groups)
  PERSONA.md             Default persona
  skills/                Stock skills (83 portable, 1 arizuko-only)
  output-styles/         Per-surface response-length rules
  cmd/ant/               Standalone CLI stub (not shipped)
  pkg/agent/loader.go    Folder layout resolver (standalone-ant)
  Dockerfile             Builds arizuko-ant:latest (TS)
```
