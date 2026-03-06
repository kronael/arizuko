# Extensibility

How external code registers with kanipi's subsystems. The goal:
extension directories that the core discovers and loads, packaged
away from core source. Agent self-modification via the same mechanism.

## Extension points

All kanipi subsystems that accept external contributions:

| Registry              | Current pattern          | Extension model        |
| --------------------- | ------------------------ | ---------------------- |
| actions (IPC/MCP/cmd) | hardcoded dispatch       | action registry (v1)   |
| channels              | compiled-in, conditional | drop-in (v2)           |
| mime handlers         | hardcoded array          | drop-in (v2)           |
| volume mounts         | hardcoded push list      | mount providers (v2)   |
| agent hooks           | hardcoded SDK object     | hook providers (v2)    |
| inbound stages        | sequential inline        | stage registry (v2)    |
| system msg producers  | scattered enqueue calls  | producer registry (v2) |

### Shipped (v1)

**Actions** — `specs/v1/actions.md`. Typed registry with Zod schemas.
MCP tools auto-generated. Commands call actions directly. IPC dispatch
looks up actions by `type`. Adding an action = one file, no wiring.

**Commands** — `src/commands/index.ts`. `registerCommand()` array.
Already a proper registry. Commands become thin action wrappers
when actions ship.

**Groups** — DB-backed. `register_group` IPC action adds groups at
runtime. Agent can register new groups via MCP.

### Open questions (v1 → v2)

**Channels**: currently `channels.push(new TelegramChannel(...))` in
`index.ts`. Adding a channel = new source file + conditional init.
Extension model: `channels/` scanned at startup, each exports a
factory. Env presence check stays per-channel. Needs: factory
interface, hot-reload on reconnect? Dynamic import?

**MIME handlers**: currently `[voiceHandler, videoHandler]` passed
to `processAttachments()`. Extension model: `mime-handlers/` scanned,
each exports an `AttachmentHandler`. Discovery at startup, no runtime
registration needed.

**Volume mounts**: currently `buildVolumeMounts()` with hardcoded
push calls. Extension model: mount providers declare what they need:

```typescript
interface MountProvider {
  name: string;
  mounts(group: RegisteredGroup): VolumeMount[];
}
```

Providers registered at startup. `buildVolumeMounts` iterates them.
Core mounts (group, ipc, self) are built-in providers.

**Agent hooks**: SDK already has the registry (`hooks: { PreCompact:
[...], PreToolUse: [...] }`). Extension = adding entries. Agent-runner
could scan a hooks directory and load hook factories. Low priority —
only two hooks exist and adding a third is one line.

**Inbound stages**: `processGroupMessages()` is 10 sequential steps.
Extension model: stage registry with ordered stages:

```typescript
interface InboundStage {
  name: string;
  order: number;
  run(ctx: InboundContext): Promise<void>;
}
```

Stages mutate a shared context (system messages, prompt parts,
enrichments). Current inline steps become built-in stages. New stages
register at startup. Sequential execution, sorted by order.

**System message producers**: any subsystem can call
`enqueueSystemMessage()`. No registry needed — the queue is the
shared interface. New producers just call the function.

## Extension directory convention

Extensions live outside core source:

```
extensions/
  <name>/
    manifest.json
    *.ts
```

Manifest declares what the extension provides:

```json
{
  "name": "image-ocr",
  "version": "1.0.0",
  "provides": {
    "mime-handler": { "entry": "handler.ts" },
    "mount": { "path": "/workspace/ocr-models", "readonly": true },
    "action": { "entry": "action.ts" }
  }
}
```

Gateway scans `extensions/` at startup. Each `provides` key maps to
a registry. The extension's entry file exports the expected interface
(AttachmentHandler, MountProvider, Action, etc).

Disabled by renaming the directory (`image-ocr` → `_image-ocr`) or
adding `"enabled": false` to manifest.

## Agent self-modification

The agent can create extensions in its writable workspace:

1. Agent writes files to `/workspace/group/extensions/<name>/`
2. Agent emits `plugin-propose` IPC (see `specs/v1/plugins.md`)
3. Operator approves
4. Gateway copies to `extensions/` directory
5. Next gateway restart picks it up

For agent-side extensions (hooks, skills), no approval needed — the
agent's `.claude/` directory is persistent and writable. Changes take
effect on next session spawn.

## Trust boundaries

| Extension type | Who writes | Who approves   | When loaded     |
| -------------- | ---------- | -------------- | --------------- |
| skills         | agent      | nobody (agent) | next spawn      |
| agent hooks    | agent      | nobody (agent) | next spawn      |
| actions        | operator   | operator       | gateway restart |
| channels       | operator   | operator       | gateway restart |
| mime handlers  | operator   | operator       | gateway restart |
| mounts         | agent      | operator       | next spawn      |
| inbound stages | operator   | operator       | gateway restart |

Agent-side extensions (skills, hooks) are sandboxed inside the
container — they can only affect the agent's own behavior. Gateway-side
extensions (actions, channels, handlers, stages) require operator
involvement because they run with gateway privileges.

## Implementation order

1. **Actions registry** — v1, specced in actions.md
2. **Extension directory scanning** — v2, enables all other registries
3. **Mount providers** — v2, after extension scanning
4. **MIME handler discovery** — v2, after extension scanning
5. **Inbound stage registry** — v2, when plugin-contributed stages needed
6. **Channel drop-in** — v2/v3, most complex (lifecycle, reconnection)
