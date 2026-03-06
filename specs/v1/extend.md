# Extensibility

How kanipi gets extended. Two surfaces:

- **Agent-side** — Claude Code SDK mechanisms. See `extend-agent.md`.
- **Gateway-side** — compiled TypeScript registries (this doc).

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
abstracting — 10 steps, clear flow. See `specs/v1/router.md`.

### Volume mounts (hardcoded, no registry)

`buildVolumeMounts()` in `container-runner.ts`. Adding a mount =
adding a push call. 10 mounts with clear conditionals. Not worth
abstracting. See `specs/v1/router.md`.

## Design principles

- **Lean on the SDK** — don't build extension mechanisms that Claude
  Code already provides.
- **Keep hardcoded what only developers touch** — inbound pipeline,
  volume mounts, agent hooks.
- **Registries where external code lives** — actions, commands,
  channels, MIME handlers. Clear interfaces, own directories.
- **No custom framework** — no manifest files, no directory scanning,
  no runtime plugin loading. Boring code.
