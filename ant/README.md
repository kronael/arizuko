# ant

The agent container runtime for arizuko. Wraps Claude Code (via
`@anthropic-ai/claude-agent-sdk`), injects group identity + skills +
memory, and exposes MCP tools over a gated unix socket.

## What it does

Every arizuko group runs in a fresh Docker container on each turn.
`ant/src/index.ts` is the entrypoint: it reads stdin for the prompt,
loads the group's `PERSONA.md` / `CLAUDE.md` / `~/.claude/settings.json`,
assembles MCP servers (core `arizuko` server via socat to `gated.sock` +
third-party connectors from `settings.json`), runs the Claude Code SDK,
and delivers the result via the `submit_turn` JSON-RPC method back to
`routd`.

## TS runtime (`src/`)

The shipping runtime uses `@anthropic-ai/claude-agent-sdk` 0.3.153.
MCP servers are assembled in `src/mcp-servers.ts`:

- **`arizuko` core server** — socat bridge to `/run/ipc/gated.sock`,
  `alwaysLoad: true` so `send`/`reply`/`inspect_*`/`send_file` stay
  eager every turn.
- **Third-party connectors** — loaded from `~/.claude/settings.json`
  (agent-registered or operator-seeded), `alwaysLoad` omitted so the
  SDK defers them behind Tool Search Tool. Large platform catalogs
  no longer flood context. Spec 6/A.

## Folder layout (agent view)

The container mounts the group folder at `/home/node/`:

```
/home/node/
  PERSONA.md          operator-owned identity overlay
  CLAUDE.md           operator-owned runbook overlay
  .claude/
    CLAUDE.md         agent-managed, merged on /migrate
    settings.json     outputStyle, mcpServers
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
(`specs/12/b-ant-standalone.md`) but doesn't run yet — all skills
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

## Layout

```
ant/
  src/index.ts           Entrypoint (stdin → SDK → submit_turn)
  src/mcp-servers.ts     MCP server assembly (eager vs deferred)
  src/backend/           SDK session wrappers
  CLAUDE.md              Agent identity + runbook (seeded to groups)
  PERSONA.md             Default persona
  skills/                Stock skills (83 portable, 1 arizuko-only)
  output-styles/         Per-surface response-length rules
  cmd/ant/               Standalone CLI stub (not shipped)
  pkg/agent/loader.go    Folder layout resolver (standalone-ant)
  Dockerfile             Builds arizuko-ant:latest (TS)
```
