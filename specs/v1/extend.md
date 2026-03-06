# Extensibility

How kanipi gets extended — by agents, by developers, by operators.
Two separate surfaces: agent-side (Claude Code SDK) and gateway-side
(compiled TypeScript).

## Agent self-extension

The agent runs Claude Code inside a container. It can extend itself
using SDK-native mechanisms. All persist across sessions via the
writable `.claude/` directory.

| Mechanism    | Path                                 | Effect                             |
| ------------ | ------------------------------------ | ---------------------------------- |
| Skills       | `.claude/skills/<name>/SKILL.md`     | New capabilities, loaded by SDK    |
| Instructions | `.claude/CLAUDE.md`                  | Changes own behavior/personality   |
| Memory       | `.claude/projects/*/memory/`         | Persists knowledge across sessions |
| MCP servers  | `.claude/settings.json` `mcpServers` | New tools available to agent       |
| Settings     | `.claude/settings.json`              | SDK configuration                  |

The agent can create skills, edit its own instructions, write memory
files, and potentially register new MCP servers — all without gateway
involvement. Changes take effect on next session spawn.

### What the agent cannot extend

- **Gateway actions** — IPC dispatch, MCP tool definitions. These
  run on the gateway, not in the container.
- **Channels** — messaging platform connectors. Gateway-side code.
- **MIME handlers** — media processing. Gateway-side code.
- **Volume mounts** — container filesystem. Set by gateway before spawn.
- **Inbound pipeline** — message processing steps. Gateway-side code.

These require developer code changes (see below).

### Skill seeding

Gateway seeds skills from `container/skills/` into `.claude/skills/`
on first spawn per group. The `/migrate` skill propagates updates
across groups. See `specs/v1/skills.md`.

### MCP server self-registration (open)

The agent could add MCP servers to its own `.claude/settings.json`.
The SDK reads `settingSources: ['project', 'user']` — project-level
settings should be picked up. Needs verification:

- Does the SDK merge `mcpServers` from settings with those passed
  to `query()`?
- If so, the agent can install an MCP server binary to its workspace
  and register it via settings. No gateway changes needed.
- Security: agent can only run binaries inside its container. The
  MCP server runs with the same sandboxing as the agent itself.

## Gateway registries

Architectural reference for developers extending kanipi's core.

| Registry      | Location                              | How to extend                                    |
| ------------- | ------------------------------------- | ------------------------------------------------ |
| Actions       | `src/actions/` (planned)              | Add action file, auto-registers                  |
| Commands      | `src/commands/`                       | `registerCommand()` at startup                   |
| Channels      | `src/channels/`                       | New file + conditional init in `index.ts`        |
| MIME handlers | `src/mime-handlers/`                  | New handler + add to array in `mime-enricher.ts` |
| Agent hooks   | `container/agent-runner/src/index.ts` | Add to `hooks:` object                           |

### Actions (v1 — specced)

Typed registry with Zod schemas. MCP tools auto-generated. Commands
call actions directly. IPC dispatch looks up by `type`. Adding an
action = one file. See `specs/v1/actions.md`.

### Commands (shipped)

`CommandHandler[]` with `registerCommand()`. One file per command in
`src/commands/`. Already a proper registry.

### Channels (shipped, compiled-in)

One class per channel in `src/channels/`. Each implements `Channel`
interface. Loaded conditionally by token presence. Adding a channel:
new file, implement interface, push to `channels[]` in `index.ts`.
See `specs/v1/channels.md`.

### MIME handlers (shipped, compiled-in)

`AttachmentHandler` interface. One file per handler in
`src/mime-handlers/`. Currently two: voice (whisper), video (ffmpeg +
whisper). Adding a handler: new file, implement interface, add to
handler array in `mime-enricher.ts`. See `specs/v1/mime.md`.

### Inbound pipeline (hardcoded, no registry)

`processGroupMessages()` in `index.ts`. Sequential steps with data
dependencies. Adding a step = editing the function inline. Not worth
abstracting for v1 — 10 steps, clear flow. See `specs/v1/router.md`.

### Volume mounts (hardcoded, no registry)

`buildVolumeMounts()` in `container-runner.ts`. Adding a mount =
adding a push call. 10 mounts with clear conditionals. Not worth
abstracting. See `specs/v1/router.md`.

## Design principles

- **Lean on the SDK** — don't build extension mechanisms that Claude
  Code already provides. Skills, memory, settings are the agent's
  primary self-extension tools.
- **Keep hardcoded what only developers touch** — inbound pipeline,
  volume mounts, agent hooks. These change rarely and have data
  dependencies that make abstraction harmful.
- **Registries where external code lives** — actions, commands,
  channels, MIME handlers. These have clear interfaces and grow
  independently. Each in its own directory.
- **No custom framework** — no manifest files, no directory scanning,
  no runtime plugin loading. Each registry has its own simple pattern.
  Boring code.
