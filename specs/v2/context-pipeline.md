# Context pipeline — open

A pluggable pipeline for injecting context into agent invocations.
Minimal abstractions, extensible via plugins later.

## Problem

Context injection is currently ad-hoc in two places:

- **Per-message**: mime handlers push strings into `_annotations[]` which
  are prepended to `input.prompt` in `container-runner.ts`. Works, but
  only mime handlers can use it — no general hook point.
- **Per-session**: CLAUDE.md seeding, settings.json, pointer injection
  (proposed) are all hardcoded in `container-runner.ts` with no interface.

Both need to be pluggable so memory layers, plugins, and custom logic can
inject context without modifying core gateway code.

## Two pipelines

### 1. Message context pipeline

Runs on every invocation. Each hook receives the current input and returns
a string to prepend to the prompt, or null.

```typescript
interface MessageContextHook {
  name: string;
  run(input: MessageContextInput): Promise<string | null>;
}

interface MessageContextInput {
  chatJid: string;
  groupFolder: string;
  groupDir: string; // host path to group workspace
  messages: NewMessage[]; // messages being sent this invocation
  isNewSession: boolean;
}
```

Output strings are collected and prepended to `input.prompt` in order,
separated by newlines. Replaces the current `_annotations` ad-hoc array.

**Built-in hooks** (in registration order):

1. `voice` — transcribes audio attachments → `[voice: ...]`
2. `video` — transcribes video attachments → `[video: ...]`
3. `session-pointer` — injects last-session pointer on new session

### 2. Session context pipeline

Runs once when a **new session** starts (no stored session ID, or resume
failed). Each hook returns a string to prepend to the first prompt only,
or null.

```typescript
interface SessionContextHook {
  name: string;
  run(input: SessionContextInput): Promise<string | null>;
}

interface SessionContextInput {
  chatJid: string;
  groupFolder: string;
  groupDir: string;
  sessionId: string | undefined; // undefined = genuinely new
}
```

**Built-in hooks**:

1. `session-pointer` — reads last archive from `conversations/`, builds
   ≤100-word pointer with path to full transcript

The session pipeline runs before the message pipeline. Both outputs are
prepended to the first prompt together.

## Registration

Hooks are registered at gateway startup in `src/context.ts`:

```typescript
export const messageHooks: MessageContextHook[] = [
  voiceHook,
  videoHook,
  sessionPointerHook,
];

export const sessionHooks: SessionContextHook[] = [sessionPointerHook];
```

Plugins (future) append to these arrays after gateway init.

## Integration points

`container-runner.ts:runContainerAgent`:

1. Run session pipeline if `isNewSession` → collect session preamble
2. Run message pipeline → collect message annotations
3. Combine: `[session preamble]\n\n[message annotations]\n\n[prompt]`
4. Write to stdin

The existing `_annotations[]` mechanism is replaced by the message pipeline.

## What does not go here

- `CLAUDE.md` seeding — filesystem setup, not context injection; stays in
  `buildVolumeMounts`
- `settings.json` env injection — container config, not prompt context;
  stays in `buildVolumeMounts`
- IPC (send_message, send_file, schedule_task) — outbound from agent, not
  inbound context; stays in `ipc.ts`

## Plugin extension point

In the plugins spec (`specs/v2/plugins.md`), plugins register hooks:

```typescript
plugin.onMessageContext(async (input) => {
  // return string or null
});

plugin.onSessionContext(async (input) => {
  // return string or null
});
```

Gateway appends plugin hooks to the arrays at startup. No core changes
needed to add new context sources.
